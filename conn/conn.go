// Package conn sends and receives Minecraft packets over a packet transport,
// such as a Helix raknet or nethernet connection. It handles batching,
// compression and encryption, and exposes hooks on every packet. It does not
// run the login or game handshake: that is up to the server.
package conn

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/helix-bedrock/protocol/login"
	"github.com/helix-bedrock/protocol/packet"
	"github.com/helix-bedrock/protocol/version"
)

// Transport carries whole batches. *raknet.Conn and *nethernet.Conn both
// implement it. A transport may also implement packet.BatchHeaderer and
// packet.EncryptionDisabler, as nethernet does.
type Transport interface {
	net.Conn
	// ReadPacket reads one whole batch.
	ReadPacket() ([]byte, error)
}

// Config configures a Conn. The zero value is valid.
type Config struct {
	// Protocol is the Minecraft version spoken on the wire. Packets read are
	// converted to the latest version and packets written are converted from
	// it, so callers only see latest packets. If nil, version.Latest.
	Protocol version.Protocol
	// Pool decodes packets by ID. If nil, Protocol.Packets(true): the packets
	// a client sends, which is what a server reads.
	Pool packet.Pool
	// ShieldID is the item runtime ID of the shield, needed to decode items.
	ShieldID int32
	// DisableLimits turns off the slice length limits when decoding packets.
	DisableLimits bool
	// FlushInterval sends buffered packets this often. If zero, packets are
	// only sent when Flush is called.
	FlushInterval time.Duration
	// Log receives errors from hooks and the flush loop. If nil, slog.Default.
	Log *slog.Logger
	// Shared holds hooks that run for every Conn created with this Config,
	// before the Conn's own hooks. A server sets them once for all players.
	Shared *Hooks
}

// ErrClosed is returned by operations on a closed Conn.
var ErrClosed = errors.New("conn: closed")

// Conn is a Minecraft packet connection. Reads must happen from one goroutine;
// writes and Flush are safe from any goroutine.
type Conn struct {
	t      Transport
	conf   Config
	log    *slog.Logger
	hooks  Hooks
	shared *Hooks

	dec *packet.Decoder
	// pending holds packets decoded from the last batch, not yet returned.
	pending [][]byte
	// converted holds latest packets made from one read packet, not yet
	// returned.
	converted []packet.Packet
	// latest is true when no conversion is needed.
	latest bool

	identity atomic.Pointer[login.IdentityData]
	client   atomic.Pointer[login.ClientData]
	game     atomic.Pointer[version.GameData]

	wmu    sync.Mutex
	enc    *packet.Encoder
	buf    [][]byte
	closed bool

	once sync.Once
	done chan struct{}
}

// New wraps t in a Conn. Compression and encryption start disabled.
func New(t Transport, conf Config) *Conn {
	if conf.Protocol == nil {
		conf.Protocol = version.Latest
	}
	if conf.Pool == nil {
		conf.Pool = conf.Protocol.Packets(true)
	}
	if conf.Log == nil {
		conf.Log = slog.Default()
	}
	c := &Conn{t: t, conf: conf, log: conf.Log, shared: conf.Shared, latest: conf.Protocol == version.Latest, dec: packet.NewDecoder(t), enc: packet.NewEncoder(t), done: make(chan struct{})}
	if conf.FlushInterval > 0 {
		go c.flushLoop(conf.FlushInterval)
	}
	return c
}

// Transport returns the underlying transport.
func (c *Conn) Transport() Transport { return c.t }

// LocalAddr returns the local address of the transport.
func (c *Conn) LocalAddr() net.Addr { return c.t.LocalAddr() }

// RemoteAddr returns the remote address of the transport.
func (c *Conn) RemoteAddr() net.Addr { return c.t.RemoteAddr() }

// EnableCompression compresses every batch sent from now on whose size is at
// least threshold, and expects compressed batches from the other side.
func (c *Conn) EnableCompression(alg packet.Compression, threshold int) {
	c.wmu.Lock()
	c.enc.EnableCompression(alg, threshold)
	c.wmu.Unlock()
	c.dec.EnableCompression(alg, packet.DefaultMaxDecompressedLen)
}

// EnableLegacyCompression switches to the batch format of versions before
// RequestNetworkSettings: every batch is compressed with alg and carries no
// compression byte. See version.LegacyNetworkSettingsProtocol.
func (c *Conn) EnableLegacyCompression(alg packet.Compression) {
	c.wmu.Lock()
	c.enc.EnableLegacyCompression(alg)
	c.wmu.Unlock()
	c.dec.EnableLegacyCompression(alg, packet.DefaultMaxDecompressedLen)
}

// EnableEncryption encrypts every batch from now on with key, using the
// version's own encryption if it implements version.EncryptionProtocol.
// Transports that disable encryption, like nethernet, ignore it.
func (c *Conn) EnableEncryption(key [32]byte) {
	if p, ok := c.conf.Protocol.(version.EncryptionProtocol); ok {
		c.wmu.Lock()
		c.enc.EnableEncryptionWith(p.Encryption(key))
		c.wmu.Unlock()
		c.dec.EnableEncryptionWith(p.Encryption(key))
		return
	}
	c.wmu.Lock()
	c.enc.EnableEncryption(key)
	c.wmu.Unlock()
	c.dec.EnableEncryption(key)
}

// ReadRaw returns the next packet as raw bytes: its header followed by its
// payload. Raw-in hooks run first and may change or drop it.
func (c *Conn) ReadRaw() ([]byte, error) {
	for {
		if len(c.pending) == 0 {
			batch, err := c.dec.Decode()
			if err != nil {
				return nil, err
			}
			c.pending = batch
			continue
		}
		b := c.pending[0]
		c.pending[0] = nil
		c.pending = c.pending[1:]
		b, keep := c.runRaw(true, b)
		if keep {
			return b, nil
		}
	}
}

// ReadPacket returns the next packet decoded. Packets with an ID the pool does
// not know are returned as *packet.Unknown. Packet-in hooks run after
// decoding and may change or drop it. A packet that fails to decode is an
// error; the connection can still be read after it.
func (c *Conn) ReadPacket() (packet.Packet, error) {
	for {
		if len(c.converted) > 0 {
			pk := c.converted[0]
			c.converted[0] = nil
			c.converted = c.converted[1:]
			if c.runPacket(true, packet.Header{PacketID: pk.ID()}, &pk) {
				return pk, nil
			}
			continue
		}
		b, err := c.ReadRaw()
		if err != nil {
			return nil, err
		}
		pk, h, err := c.decode(b)
		if err != nil {
			return nil, err
		}
		if c.latest {
			if c.runPacket(true, h, &pk) {
				return pk, nil
			}
			continue
		}
		c.converted = c.conf.Protocol.ConvertToLatest(pk, c)
	}
}

// Protocol returns the Minecraft version spoken on the wire.
func (c *Conn) Protocol() version.Protocol { return c.conf.Protocol }

// SetLoginData stores the identity and client data the player logged in
// with, which version conversions may need. The server calls it after login.
func (c *Conn) SetLoginData(id login.IdentityData, cd login.ClientData) {
	c.identity.Store(&id)
	c.client.Store(&cd)
}

// SetGameData stores the game data sent to the player in StartGame, which
// version conversions may need. The server calls it when the player joins.
func (c *Conn) SetGameData(d version.GameData) {
	c.game.Store(&d)
}

// GameData returns the data set with SetGameData, or zero values.
func (c *Conn) GameData() version.GameData {
	if p := c.game.Load(); p != nil {
		return *p
	}
	return version.GameData{}
}

// IdentityData returns the data set with SetLoginData, or zero values.
func (c *Conn) IdentityData() login.IdentityData {
	if p := c.identity.Load(); p != nil {
		return *p
	}
	return login.IdentityData{}
}

// ClientData returns the data set with SetLoginData, or zero values.
func (c *Conn) ClientData() login.ClientData {
	if p := c.client.Load(); p != nil {
		return *p
	}
	return login.ClientData{}
}

// decode decodes one raw packet.
func (c *Conn) decode(b []byte) (packet.Packet, packet.Header, error) {
	var h packet.Header
	r := bytes.NewReader(b)
	if err := h.Read(r); err != nil {
		return nil, h, fmt.Errorf("read packet header: %w", err)
	}
	payload := b[len(b)-r.Len():]
	f, ok := c.conf.Pool[h.PacketID]
	if !ok {
		return &packet.Unknown{PacketID: h.PacketID, Payload: append([]byte(nil), payload...)}, h, nil
	}
	pk := f()
	buf := bytes.NewBuffer(payload)
	pr := c.conf.Protocol.NewReader(buf, c.conf.ShieldID, !c.conf.DisableLimits)
	if err := packet.DecodeWith(pk, pr, buf); err != nil {
		return nil, h, err
	}
	return pk, h, nil
}

// WritePacket encodes pk and buffers it until the next flush. Packet-out hooks
// run first and may change or drop it.
func (c *Conn) WritePacket(pk packet.Packet) error {
	if !c.runPacket(false, packet.Header{PacketID: pk.ID()}, &pk) {
		return nil
	}
	if c.latest {
		return c.encode(pk)
	}
	for _, out := range c.conf.Protocol.ConvertFromLatest(pk, c) {
		if err := c.encode(out); err != nil {
			return err
		}
	}
	return nil
}

// encode encodes pk in the wire version and buffers it.
func (c *Conn) encode(pk packet.Packet) error {
	buf := bytes.NewBuffer(make([]byte, 0, 64))
	h := packet.Header{PacketID: pk.ID()}
	if err := h.Write(buf); err != nil {
		return err
	}
	if err := packet.EncodeWith(pk, c.conf.Protocol.NewWriter(buf, c.conf.ShieldID)); err != nil {
		return err
	}
	return c.WriteRaw(buf.Bytes())
}

// WriteRaw buffers b, a raw packet (header and payload), until the next flush.
// Raw-out hooks run first and may change or drop it.
func (c *Conn) WriteRaw(b []byte) error {
	b, keep := c.runRaw(false, b)
	if !keep {
		return nil
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return ErrClosed
	}
	c.buf = append(c.buf, b)
	return nil
}

// Flush sends every buffered packet as one batch.
func (c *Conn) Flush() error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.flushLocked()
}

func (c *Conn) flushLocked() error {
	if c.closed {
		return ErrClosed
	}
	if len(c.buf) == 0 {
		return nil
	}
	err := c.enc.Encode(c.buf)
	clear(c.buf)
	c.buf = c.buf[:0]
	return err
}

func (c *Conn) flushLoop(d time.Duration) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			if err := c.Flush(); err != nil && !errors.Is(err, ErrClosed) {
				c.log.Debug("conn: flush", "err", err, "remote", c.t.RemoteAddr())
			}
		}
	}
}

// Close flushes buffered packets and closes the transport. It is safe to call
// more than once.
func (c *Conn) Close() error {
	var err error
	c.once.Do(func() {
		close(c.done)
		c.wmu.Lock()
		_ = c.flushLocked()
		c.closed = true
		c.wmu.Unlock()
		err = c.t.Close()
		c.runClose()
	})
	return err
}
