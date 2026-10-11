package packet

import (
	"bytes"
	"fmt"

	"github.com/helix-bedrock/protocol"
)

// Decode reads pk from data, the packet payload without its header. It never
// panics: invalid data returns an error. If data has bytes left over after the
// packet, Decode returns an error too.
func Decode(pk Packet, data []byte, shieldID int32, enableLimits bool) error {
	buf := bytes.NewBuffer(data)
	return DecodeWith(pk, protocol.NewReader(buf, shieldID, enableLimits), buf)
}

// DecodeWith reads pk using r, which reads from buf. It lets another protocol
// version supply its own reader. Like Decode, it never panics and fails if
// buf has bytes left over.
func DecodeWith(pk Packet, r protocol.IO, buf interface{ Len() int }) (err error) {
	defer func() {
		// Last line of defence: a bug in a packet's Marshal (for example an
		// index out of range on bad input) must not crash the server.
		if v := recover(); v != nil {
			err = fmt.Errorf("decode packet %T: %v", pk, v)
		}
	}()
	pk.Marshal(r)
	if err := r.Err(); err != nil {
		return fmt.Errorf("decode packet %T: %w", pk, err)
	}
	if buf.Len() != 0 {
		return fmt.Errorf("decode packet %T: %v unread bytes", pk, buf.Len())
	}
	return nil
}

// Encode appends pk's payload, without its header, to buf. It never panics.
func Encode(pk Packet, buf *bytes.Buffer, shieldID int32) error {
	return EncodeWith(pk, protocol.NewWriter(buf, shieldID))
}

// EncodeWith writes pk using w, which another protocol version may supply.
// It never panics.
func EncodeWith(pk Packet, w protocol.IO) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("encode packet %T: %v", pk, v)
		}
	}()
	pk.Marshal(w)
	if err := w.Err(); err != nil {
		return fmt.Errorf("encode packet %T: %w", pk, err)
	}
	return nil
}
