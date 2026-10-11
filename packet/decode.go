package packet

import (
	"bytes"
	"fmt"

	"github.com/helix-bedrock/protocol"
)

// Decode reads pk from data, the packet payload without its header. It never
// panics: invalid data returns an error. If data has bytes left over after the
// packet, Decode returns an error too.
func Decode(pk Packet, data []byte, shieldID int32, enableLimits bool) (err error) {
	defer func() {
		// Last line of defence: a bug in a packet's Marshal (for example an
		// index out of range on bad input) must not crash the server.
		if v := recover(); v != nil {
			err = fmt.Errorf("decode packet %T: %v", pk, v)
		}
	}()
	buf := bytes.NewBuffer(data)
	r := protocol.NewReader(buf, shieldID, enableLimits)
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
func Encode(pk Packet, buf *bytes.Buffer, shieldID int32) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("encode packet %T: %v", pk, v)
		}
	}()
	w := protocol.NewWriter(buf, shieldID)
	pk.Marshal(w)
	if err := w.Err(); err != nil {
		return fmt.Errorf("encode packet %T: %w", pk, err)
	}
	return nil
}
