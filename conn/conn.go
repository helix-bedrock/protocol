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

	"github.com/helix-bedrock/protocol"
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
	// MaxBatchSize is the largest decompressed batch accepted. If 0,
	// packet.DefaultMaxDecompressedLen. BDS 1.26.60 allows 10 MiB.
	MaxBatchSize int
	// DisableLimits turns off the slice length limits when decoding packets.
	DisableLimits bool
	// ReusePackets are the IDs of the packets this Conn reuses: one handed back with
	// Release is cleared and read into again, so reading those packets allocates nothing
	// once warm. For packets read many times a second (PlayerAuthInput). The caller must
	// not keep such a packet, or anything in it, after releasing it.
	ReusePackets []uint32
	// FromServer is set on a client's connection, which reads what a server sends: a
	// server may put any number of packets in one batch (a player joining a full server
	// gets one about every player), so the 812 packet limit on batches from clients is
	// off.
	FromServer bool
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
	// batch holds the packets of the last batch read, pending views of those not yet
	// returned and pendingAll all of them, payload the reader over the packet being
	// decoded and reader the decoder for the latest version: all reused, so reading
	// allocates nothing but the packets themselves. Read side only.
	batch      []byte
	pending    [][]byte
	pendingAll [][]byte
	payload    bytes.Reader
	reader     *protocol.Reader
	// reuse is ReusePackets as a set, and free the released packets by ID waiting to be
	// read into again, under freeMu: Release is called from another goroutine.
	reuse  map[uint32]bool
	freeMu sync.Mutex
	free   map[uint32][]packet.Packet
	// converted holds latest packets made from one read packet, not yet
	// returned.
	converted []packet.Packet
	// latest is true when no conversion is needed.
	latest bool

	identity atomic.Pointer[login.IdentityData]
	client   atomic.Pointer[login.ClientData]
	game     atomic.Pointer[version.GameData]

	// omu serializes the whole write path, hooks and conversion included, so
	// outgoing hooks and the version converter never run twice at once on one
	// Conn. It is taken before wmu, never after.
	omu sync.Mutex
	wmu sync.Mutex
	enc *packet.Encoder
	// raw encodes one packet for the latest version, reused; under omu.
	raw *packet.RawWriter
	// arena holds every packet buffered since the last flush back to back, ends where
	// each one stops, and buf the views of them handed to the encoder: all reused, so
	// buffering a packet allocates nothing once they have grown. Under wmu.
	arena  []byte
	ends   []int
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
	if conf.FromServer {
		c.dec.DisableBatchPacketLimit()
	}
	if len(conf.ReusePackets) > 0 {
		c.reuse, c.free = make(map[uint32]bool, len(conf.ReusePackets)), make(map[uint32][]packet.Packet, len(conf.ReusePackets))
		for _, id := range conf.ReusePackets {
			c.reuse[id] = true
		}
	}
	if conf.FlushInterval > 0 {
		go c.flushLoop(conf.FlushInterval)
	}
	return c
}

// maxBatch returns the decompressed batch size limit.
func (c *Conn) maxBatch() int {
	if c.conf.MaxBatchSize > 0 {
		return c.conf.MaxBatchSize
	}
	return packet.DefaultMaxDecompressedLen
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
	c.dec.EnableCompression(alg, c.maxBatch())
}

// EnableLegacyCompression switches to the batch format of versions before
// RequestNetworkSettings: every batch is compressed with alg and carries no
// compression byte. See version.LegacyNetworkSettingsProtocol.
func (c *Conn) EnableLegacyCompression(alg packet.Compression) {
	c.wmu.Lock()
	c.enc.EnableLegacyCompression(alg)
	c.wmu.Unlock()
	c.dec.EnableLegacyCompression(alg, c.maxBatch())
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

// readBatch reads the next batch into the reused buffers.
func (c *Conn) readBatch() error {
	clear(c.pendingAll)
	batch, all, err := c.dec.DecodeAppend(c.batch[:0], c.pendingAll[:0])
	if cap(batch) > maxKeptArena {
		// One huge batch (a chunk burst) must not pin its memory for the whole session.
		c.batch, c.pendingAll = nil, nil
	} else {
		c.batch, c.pendingAll = batch, all
	}
	if err != nil {
		c.pending = nil
		return err
	}
	c.pending = all
	return nil
}

// ReadRaw returns the next packet as raw bytes: its header followed by its
// payload. Raw-in hooks run first and may change or drop it. The bytes are only
// valid until the next read: copy them to keep them.
func (c *Conn) ReadRaw() ([]byte, error) {
	for {
		if len(c.pending) == 0 {
			if err := c.readBatch(); err != nil {
				return nil, err
			}
			continue
		}
		b := c.pending[0]
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

// decode decodes one raw packet: the header and the payload from one reused reader.
func (c *Conn) decode(b []byte) (packet.Packet, packet.Header, error) {
	var h packet.Header
	r := &c.payload
	r.Reset(b)
	if err := h.Read(r); err != nil {
		return nil, h, fmt.Errorf("read packet header: %w", err)
	}
	f, ok := c.conf.Pool[h.PacketID]
	if !ok {
		return &packet.Unknown{PacketID: h.PacketID, Payload: append([]byte(nil), b[len(b)-r.Len():]...)}, h, nil
	}
	pk := c.newPacket(h.PacketID, f)
	if err := packet.DecodeWith(pk, c.packetReader(), r); err != nil {
		c.Release(pk)
		return nil, h, err
	}
	return pk, h, nil
}

// maxFreePackets is how many released packets of one ID a Conn keeps.
const maxFreePackets = 64

// newPacket is a released packet of the ID to read into, or a new one.
func (c *Conn) newPacket(id uint32, f func() packet.Packet) packet.Packet {
	if !c.reuse[id] {
		return f()
	}
	c.freeMu.Lock()
	defer c.freeMu.Unlock()
	list := c.free[id]
	if len(list) == 0 {
		return f()
	}
	pk := list[len(list)-1]
	list[len(list)-1] = nil
	c.free[id] = list[:len(list)-1]
	return pk
}

// Release hands back a packet read from this Conn once the caller is done with it: one
// of Config.ReusePackets is cleared and read into again; any other is left to the
// garbage collector. Nothing of it may be used afterwards. Safe from any goroutine.
func (c *Conn) Release(pk packet.Packet) {
	if pk == nil || !c.reuse[pk.ID()] {
		return
	}
	packet.Reset(pk)
	c.freeMu.Lock()
	defer c.freeMu.Unlock()
	if list := c.free[pk.ID()]; len(list) < maxFreePackets {
		c.free[pk.ID()] = append(list, pk)
	}
}

// packetReader is the reader decode reads a payload with: one reused reader for the
// latest version, a new one from the version otherwise.
func (c *Conn) packetReader() protocol.IO {
	limits := !c.conf.DisableLimits
	if !c.latest {
		return c.conf.Protocol.NewReader(&c.payload, c.conf.ShieldID, limits)
	}
	if c.reader == nil {
		c.reader = protocol.NewReader(&c.payload, c.conf.ShieldID, limits)
	} else {
		c.reader.Reset(&c.payload, c.conf.ShieldID, limits)
	}
	return c.reader
}

// WritePacket encodes pk and buffers it until the next flush. Packet-out hooks
// run first and may change or drop it. Writes to one Conn run one at a time,
// hooks and conversion included, so an outgoing hook must not write to the
// Conn it is called for.
func (c *Conn) WritePacket(pk packet.Packet) error {
	c.omu.Lock()
	defer c.omu.Unlock()
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

// encode encodes pk in the wire version and buffers it. With omu held.
func (c *Conn) encode(pk packet.Packet) error {
	if !c.latest {
		var buf bytes.Buffer
		h := packet.Header{PacketID: pk.ID()}
		if err := h.Write(&buf); err != nil {
			return err
		}
		if err := packet.EncodeWith(pk, c.conf.Protocol.NewWriter(&buf, c.conf.ShieldID)); err != nil {
			return err
		}
		return c.writeRaw(buf.Bytes())
	}
	if c.raw == nil {
		c.raw = packet.NewRawWriter(c.conf.ShieldID)
	}
	b, err := c.raw.Encode(pk)
	if err != nil {
		return err
	}
	return c.writeRaw(b)
}

// RawOK reports whether raw packets in the latest version (packet.RawWriter's) can be
// written to this Conn with WriteRaw in place of the packets: it speaks the latest version
// and no packet-out hook would miss them. A server encoding a packet once for many
// players checks it for each. Safe from any goroutine.
func (c *Conn) RawOK() bool {
	return c.latest && len(c.hooks.packetOutList()) == 0 && len(c.shared.packetOutList()) == 0
}

// WriteRaw buffers b, a raw packet (header and payload), until the next flush.
// Raw-out hooks run first and may change or drop it. Like WritePacket, it runs
// one write at a time, so a raw-out hook must not write to its own Conn. b is
// copied, so the caller may reuse it, or write it to many Conns.
func (c *Conn) WriteRaw(b []byte) error {
	c.omu.Lock()
	defer c.omu.Unlock()
	return c.writeRaw(b)
}

// writeRaw is WriteRaw with omu held.
func (c *Conn) writeRaw(b []byte) error {
	b, keep := c.runRaw(false, b)
	if !keep {
		return nil
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed {
		return ErrClosed
	}
	c.arena = append(c.arena, b...)
	c.ends = append(c.ends, len(c.arena))
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
	if len(c.ends) == 0 {
		return nil
	}
	start := 0
	for _, end := range c.ends {
		c.buf = append(c.buf, c.arena[start:end:end])
		start = end
	}
	err := c.enc.Encode(c.buf)
	clear(c.buf)
	c.buf, c.ends = c.buf[:0], c.ends[:0]
	c.arena = c.arena[:0]
	if cap(c.arena) > maxKeptArena {
		// One huge batch (a chunk burst) must not pin its memory for the whole session.
		c.arena = nil
	}
	return err
}

// maxKeptArena is the largest buffer a Conn keeps between batches: room for a tick of
// movement with hundreds of players in view (about 13 KB for 400) and the usual packets.
// Bigger batches (chunks) use a buffer once and drop it, so hundreds of connections never
// each hold a chunk burst's worth of memory.
const maxKeptArena = 64 << 10

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
