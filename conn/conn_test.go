package conn

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/helix-bedrock/protocol/packet"
)

// memTransport is one end of an in-memory transport that carries whole
// batches.
type memTransport struct {
	in, out chan []byte
	once    *sync.Once
	closed  chan struct{}
}

func memPair() (*memTransport, *memTransport) {
	a, b := make(chan []byte, 64), make(chan []byte, 64)
	once, closed := new(sync.Once), make(chan struct{})
	return &memTransport{in: a, out: b, once: once, closed: closed}, &memTransport{in: b, out: a, once: once, closed: closed}
}

func (m *memTransport) ReadPacket() ([]byte, error) {
	select {
	case b := <-m.in:
		return b, nil
	case <-m.closed:
		return nil, net.ErrClosed
	}
}

func (m *memTransport) Read(b []byte) (int, error) {
	p, err := m.ReadPacket()
	return copy(b, p), err
}

func (m *memTransport) Write(b []byte) (int, error) {
	select {
	case <-m.closed:
		return 0, net.ErrClosed
	case m.out <- append([]byte(nil), b...):
		return len(b), nil
	}
}

func (m *memTransport) Close() error {
	m.once.Do(func() { close(m.closed) })
	return nil
}

func (m *memTransport) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (m *memTransport) RemoteAddr() net.Addr             { return &net.UDPAddr{} }
func (m *memTransport) SetDeadline(time.Time) error      { return nil }
func (m *memTransport) SetReadDeadline(time.Time) error  { return nil }
func (m *memTransport) SetWriteDeadline(time.Time) error { return nil }

// pair returns a client-side and a server-side Conn joined in memory.
func pair(t *testing.T, server Config) (client, srv *Conn) {
	t.Helper()
	a, b := memPair()
	client = New(a, Config{Pool: packet.NewServerPool()})
	srv = New(b, server)
	t.Cleanup(func() { _ = client.Close(); _ = srv.Close() })
	return client, srv
}

func send(t *testing.T, c *Conn, pks ...packet.Packet) {
	t.Helper()
	for _, pk := range pks {
		if err := c.WritePacket(pk); err != nil {
			t.Fatalf("WritePacket: %v", err)
		}
	}
	if err := c.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

func text(t *testing.T, c *Conn) string {
	t.Helper()
	pk, err := c.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	tx, ok := pk.(*packet.Text)
	if !ok {
		t.Fatalf("read %T, want *packet.Text", pk)
	}
	return tx.Message
}

func msg(s string) *packet.Text {
	return &packet.Text{TextType: packet.TextTypeChat, SourceName: "Steve", Message: s}
}

func TestBatchRoundTrip(t *testing.T) {
	client, srv := pair(t, Config{})
	send(t, client, msg("one"), msg("two"), msg("three"))
	for _, want := range []string{"one", "two", "three"} {
		if got := text(t, srv); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestCompressionAndEncryption(t *testing.T) {
	client, srv := pair(t, Config{})
	key := [32]byte{1, 2, 3}
	for _, c := range []*Conn{client, srv} {
		c.EnableCompression(packet.FlateCompression, 0)
		c.EnableEncryption(key)
	}
	long := string(bytes.Repeat([]byte("helix "), 200))
	send(t, client, msg(long), msg("short"))
	send(t, client, msg("second batch"))
	for _, want := range []string{long, "short", "second batch"} {
		if got := text(t, srv); got != want {
			t.Fatalf("got %d bytes, want %d", len(got), len(want))
		}
	}
}

func TestUnknownPacket(t *testing.T) {
	client, srv := pair(t, Config{Pool: packet.Pool{}})
	send(t, client, msg("x"))
	pk, err := srv.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}
	if u, ok := pk.(*packet.Unknown); !ok || u.PacketID != packet.IDText {
		t.Fatalf("read %#v, want Unknown text packet", pk)
	}
}

func TestBadBatchIsAnErrorNotAPanic(t *testing.T) {
	a, b := memPair()
	srv := New(b, Config{})
	t.Cleanup(func() { _ = srv.Close() })
	for _, bad := range [][]byte{{0x00}, {0xfe, 0xff, 0xff, 0xff, 0xff, 0x0f}, {0xfe, 0x05, 0x09, 0x80}} {
		_, _ = a.Write(bad)
		if _, err := srv.ReadPacket(); err == nil {
			t.Fatalf("ReadPacket(%x) returned nil error", bad)
		}
	}
	// The connection still works after bad batches.
	client := New(a, Config{Pool: packet.NewServerPool()})
	send(t, client, msg("ok"))
	if got := text(t, srv); got != "ok" {
		t.Fatalf("got %q after bad batches", got)
	}
}

func TestHooks(t *testing.T) {
	shared := &Hooks{}
	var rawIn, closed atomic.Int32
	shared.OnRawIn(func(*RawEvent) Verdict { rawIn.Add(1); return Allow })
	shared.OnClose(func(*Conn) { closed.Add(1) })
	client, srv := pair(t, Config{Shared: shared})

	// Packet-in hooks can change a packet and drop one.
	srv.Hooks().OnPacketIn(func(e *PacketEvent) Verdict {
		tx := e.Packet.(*packet.Text)
		if tx.Message == "drop" {
			return Drop
		}
		tx.Message += "!"
		return Allow
	})
	// Packet-out hooks can drop a packet; removing the hook stops that.
	remove := client.Hooks().OnPacketOut(func(e *PacketEvent) Verdict {
		if e.Packet.(*packet.Text).Message == "secret" {
			return Drop
		}
		return Allow
	})
	send(t, client, msg("secret"), msg("drop"), msg("hi"))
	if got := text(t, srv); got != "hi!" {
		t.Fatalf("got %q, want hi!", got)
	}
	remove()
	send(t, client, msg("secret"))
	if got := text(t, srv); got != "secret!" {
		t.Fatalf("got %q, want secret! after removing the hook", got)
	}
	if rawIn.Load() != 3 {
		t.Fatalf("shared raw-in hook saw %d packets, want 3", rawIn.Load())
	}
	_ = srv.Close()
	_ = srv.Close()
	if closed.Load() != 1 {
		t.Fatalf("close hook called %d times, want 1", closed.Load())
	}
}

func TestPanickingHookDoesNotCrash(t *testing.T) {
	client, srv := pair(t, Config{})
	srv.Hooks().OnPacketIn(func(*PacketEvent) Verdict { panic("broken hook") })
	send(t, client, msg("still works"))
	if got := text(t, srv); got != "still works" {
		t.Fatalf("got %q", got)
	}
}

func TestFlushInterval(t *testing.T) {
	a, b := memPair()
	client := New(a, Config{Pool: packet.NewServerPool(), FlushInterval: 10 * time.Millisecond})
	srv := New(b, Config{})
	t.Cleanup(func() { _ = client.Close(); _ = srv.Close() })
	if err := client.WritePacket(msg("auto")); err != nil {
		t.Fatal(err)
	}
	if got := text(t, srv); got != "auto" {
		t.Fatalf("got %q", got)
	}
}

func TestClosed(t *testing.T) {
	client, _ := pair(t, Config{})
	_ = client.Close()
	if err := client.WritePacket(msg("late")); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}

// FuzzReadPacket feeds random batches to a Conn. It must never panic.
func FuzzReadPacket(f *testing.F) {
	f.Add([]byte{0xfe, 0x02, 0x09, 0x01})
	f.Add([]byte{0xfe})
	f.Fuzz(func(t *testing.T, data []byte) {
		a, b := memPair()
		srv := New(b, Config{})
		defer func() { _ = srv.Close() }()
		_, _ = a.Write(data)
		_ = a.Close()
		for range 64 {
			if _, err := srv.ReadPacket(); errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return
			}
		}
	})
}

// BenchmarkWriteFlush measures sending one packet per batch with no hooks.
func BenchmarkWriteFlush(b *testing.B) {
	a, other := memPair()
	c := New(a, Config{Pool: packet.NewServerPool()})
	go func() {
		for range other.in {
		}
	}()
	pk := msg("hello")
	b.ReportAllocs()
	for b.Loop() {
		_ = c.WritePacket(pk)
		_ = c.Flush()
	}
}
