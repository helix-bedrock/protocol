package conn

import (
	"net"
	"testing"
	"time"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/helix-bedrock/protocol"
	"github.com/helix-bedrock/protocol/packet"
	"github.com/helix-bedrock/protocol/version"
)

// discardTransport takes batches and drops them, so a benchmark measures only the Conn.
type discardTransport struct{ bytes int }

func (d *discardTransport) ReadPacket() ([]byte, error)      { return nil, net.ErrClosed }
func (d *discardTransport) Read([]byte) (int, error)         { return 0, net.ErrClosed }
func (d *discardTransport) Write(b []byte) (int, error)      { d.bytes += len(b); return len(b), nil }
func (d *discardTransport) Close() error                     { return nil }
func (d *discardTransport) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (d *discardTransport) RemoteAddr() net.Addr             { return &net.UDPAddr{} }
func (d *discardTransport) SetDeadline(time.Time) error      { return nil }
func (d *discardTransport) SetReadDeadline(time.Time) error  { return nil }
func (d *discardTransport) SetWriteDeadline(time.Time) error { return nil }

// replayTransport hands out recorded batches in order, once each, for decode
// benchmarks. Decryption works in place, so each is handed out once, as a socket gives
// each batch once.
type replayTransport struct {
	discardTransport
	batches [][]byte
	next    int
}

func (r *replayTransport) ReadPacket() ([]byte, error) {
	b := r.batches[r.next%len(r.batches)]
	r.next++
	return b, nil
}

// benchKey is a fixed encryption key.
var benchKey = [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}

// movesPerBatch is about what one player's flush carries per tick with many players
// in view: one MovePlayer per player that moved.
const movesPerBatch = 50

func movePlayer(i int) *packet.MovePlayer {
	return &packet.MovePlayer{EntityRuntimeID: uint64(i + 1), Position: mgl32.Vec3{float32(i), 64, float32(i)}, Pitch: 10, Yaw: 20, HeadYaw: 20, OnGround: true, Tick: 1000}
}

// serverConn is a server's Conn to one player with compression and encryption on.
func serverConn(t Transport) *Conn {
	c := New(t, Config{Pool: version.Latest.Packets(false)})
	c.EnableCompression(packet.FlateCompression, 256)
	c.EnableEncryption(benchKey)
	return c
}

// BenchmarkWriteMovePlayer is the fan-out cost per packet: one MovePlayer encoded for
// one viewer, as the server does for every viewer of every moving player.
func BenchmarkWriteMovePlayer(b *testing.B) {
	c := serverConn(&discardTransport{})
	pk := movePlayer(1)
	b.ReportAllocs()
	for range b.N {
		_ = c.WritePacket(pk)
		c.arena, c.ends = c.arena[:0], c.ends[:0]
	}
}

// BenchmarkFlushMoves is one player's flush per tick: movesPerBatch MovePlayers encoded,
// batched, compressed and encrypted.
func BenchmarkFlushMoves(b *testing.B) {
	t := &discardTransport{}
	c := serverConn(t)
	pks := make([]*packet.MovePlayer, movesPerBatch)
	for i := range pks {
		pks[i] = movePlayer(i)
	}
	b.ReportAllocs()
	for range b.N {
		for _, pk := range pks {
			_ = c.WritePacket(pk)
		}
		_ = c.Flush()
	}
	b.ReportMetric(float64(t.bytes)/float64(b.N), "bytes/batch")
}

// BenchmarkFlushSharedMoves is the same flush when every move was encoded once for all
// viewers (Conn.EncodePacket) and only written raw here.
func BenchmarkFlushSharedMoves(b *testing.B) {
	t := &discardTransport{}
	c := serverConn(t)
	raw := encodedMoves(b)
	b.ReportAllocs()
	for range b.N {
		for _, pk := range raw {
			_ = c.WriteRaw(pk)
		}
		_ = c.Flush()
	}
}

// BenchmarkEncodeBatch is only the batch step of a flush (length prefixes, flate,
// encryption) over packets already encoded.
func BenchmarkEncodeBatch(b *testing.B) {
	enc := packet.NewEncoder(&discardTransport{})
	enc.EnableCompression(packet.FlateCompression, 256)
	enc.EnableEncryption(benchKey)
	raw := encodedMoves(b)
	b.ReportAllocs()
	for range b.N {
		_ = enc.Encode(raw)
	}
}

// BenchmarkEncodeBatchNoEncryption is the batch step without encryption, to split its
// cost from flate's.
func BenchmarkEncodeBatchNoEncryption(b *testing.B) {
	enc := packet.NewEncoder(&discardTransport{})
	enc.EnableCompression(packet.FlateCompression, 256)
	raw := encodedMoves(b)
	b.ReportAllocs()
	for range b.N {
		_ = enc.Encode(raw)
	}
}

// encodedMoves is movesPerBatch MovePlayers as raw packets.
func encodedMoves(b *testing.B) [][]byte {
	b.Helper()
	rw := packet.NewRawWriter(0)
	out := make([][]byte, 0, movesPerBatch)
	for i := range movesPerBatch {
		raw, err := rw.Append(nil, movePlayer(i))
		if err != nil {
			b.Fatal(err)
		}
		out = append(out, raw)
	}
	return out
}

// recordTransport keeps every batch written.
type recordTransport struct {
	discardTransport
	batches [][]byte
}

func (r *recordTransport) Write(b []byte) (int, error) {
	r.batches = append(r.batches, append([]byte(nil), b...))
	return len(b), nil
}

// BenchmarkReadMoves is a client reading one player's flush: decrypt, inflate, split and
// decode movesPerBatch MovePlayers. Per batch.
func BenchmarkReadMoves(b *testing.B) {
	rec := &recordTransport{}
	w := serverConn(rec)
	// The reader must see the same encryption counters, so it reads the batches in the
	// order they were written, once each: write b.N of them up front.
	for range b.N {
		for i := range movesPerBatch {
			_ = w.WritePacket(movePlayer(i))
		}
		_ = w.Flush()
	}
	r := New(&replayTransport{batches: rec.batches}, Config{Pool: version.Latest.Packets(false), FromServer: true})
	r.EnableCompression(packet.FlateCompression, 256)
	r.EnableEncryption(benchKey)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for range movesPerBatch {
			if _, err := r.ReadPacket(); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkReadAuthInput is the server reading one PlayerAuthInput, the packet every
// player sends 20 times a second: decrypt, inflate (one packet, usually under the
// threshold), decode.
func BenchmarkReadAuthInput(b *testing.B) {
	rec := &recordTransport{}
	w := New(rec, Config{Pool: version.Latest.Packets(true)})
	w.EnableCompression(packet.FlateCompression, 256)
	w.EnableEncryption(benchKey)
	in := &packet.PlayerAuthInput{Position: mgl32.Vec3{1, 65.62, 1}, Tick: 1, InputData: protocol.NewInputFlags(packet.InputFlagCount)}
	in.InputData.Set(packet.InputFlagVerticalCollision)
	in.InputData.Set(packet.InputFlagUp)
	for range b.N {
		_ = w.WritePacket(in)
		_ = w.Flush()
	}
	r := New(&replayTransport{batches: rec.batches}, Config{Pool: version.Latest.Packets(true), ReusePackets: []uint32{packet.IDPlayerAuthInput}})
	r.EnableCompression(packet.FlateCompression, 256)
	r.EnableEncryption(benchKey)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		pk, err := r.ReadPacket()
		if err != nil {
			b.Fatal(err)
		}
		r.Release(pk)
	}
}
