package packet

import (
	"bytes"
	"fmt"

	"github.com/helix-bedrock/protocol"
)

// RawWriter encodes single packets in the latest version as raw packets (header and
// payload), reusing its buffer and writer, so encoding allocates nothing once warm. A
// server encodes a packet meant for many players once with it and writes the bytes to
// each (conn.Conn.WriteRaw). Not safe for use by more than one goroutine at once.
type RawWriter struct {
	buf      bytes.Buffer
	w        *protocol.Writer
	shieldID int32
}

// NewRawWriter makes a RawWriter; shieldID is the shield's item runtime ID, which item
// encoding needs.
func NewRawWriter(shieldID int32) *RawWriter {
	rw := &RawWriter{shieldID: shieldID}
	rw.w = protocol.NewWriter(&rw.buf, shieldID)
	return rw
}

// Append encodes pk onto the end of dst and returns it.
func (rw *RawWriter) Append(dst []byte, pk Packet) ([]byte, error) {
	b, err := rw.Encode(pk)
	if err != nil {
		return dst, err
	}
	return append(dst, b...), nil
}

// maxKeptRaw is the largest buffer a RawWriter keeps between packets. One big packet (a
// join's CraftingData is about 2 MB) grows it once; kept, every connection would hold that
// for its whole life.
const maxKeptRaw = 64 << 10

// Encode encodes pk and returns the bytes, which are only valid until the next call.
func (rw *RawWriter) Encode(pk Packet) ([]byte, error) {
	if rw.buf.Cap() > maxKeptRaw {
		rw.buf = bytes.Buffer{}
	}
	rw.buf.Reset()
	h := Header{PacketID: pk.ID()}
	if err := h.Write(&rw.buf); err != nil {
		return nil, fmt.Errorf("encode %T header: %w", pk, err)
	}
	rw.w.Reset(&rw.buf, rw.shieldID)
	if err := EncodeWith(pk, rw.w); err != nil {
		return nil, err
	}
	return rw.buf.Bytes(), nil
}
