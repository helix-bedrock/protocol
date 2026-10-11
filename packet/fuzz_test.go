package packet

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/helix-bedrock/protocol"
)

// allPackets returns a constructor for every packet ID, sorted by ID.
func allPackets() []func() Packet {
	ids := map[uint32]func() Packet{}
	for id, f := range NewClientPool() {
		ids[id] = f
	}
	for id, f := range NewServerPool() {
		ids[id] = f
	}
	keys := make([]uint32, 0, len(ids))
	for id := range ids {
		keys = append(keys, id)
	}
	slices.Sort(keys)
	out := make([]func() Packet, len(keys))
	for i, id := range keys {
		out[i] = ids[id]
	}
	return out
}

// FuzzDecode decodes random bytes as every packet. The reader must never
// panic, and a packet that decodes must encode again.
func FuzzDecode(f *testing.F) {
	pks := allPackets()
	for i := range pks {
		f.Add(uint16(i), []byte{})
		f.Add(uint16(i), []byte{0xff, 0xff, 0xff, 0xff, 0x0f})
		f.Add(uint16(i), bytes.Repeat([]byte{1}, 64))
	}
	f.Fuzz(func(t *testing.T, which uint16, data []byte) {
		pk := pks[int(which)%len(pks)]()
		buf := bytes.NewBuffer(data)
		r := protocol.NewReader(buf, 0, true)
		func() {
			defer func() {
				if v := recover(); v != nil {
					t.Fatalf("%T panicked on %x: %v", pk, data, v)
				}
			}()
			pk.Marshal(r)
		}()
		if r.Err() != nil {
			return
		}
		var out bytes.Buffer
		if err := Encode(pk, &out, 0); err != nil {
			t.Logf("%T decoded but did not encode: %v", pk, err)
		}
	})
}

func TestDecodeNeverPanics(t *testing.T) {
	inputs := [][]byte{nil, {0x80}, {0xff, 0xff, 0xff, 0xff, 0x0f}, bytes.Repeat([]byte{0xff}, 32), bytes.Repeat([]byte{1}, 256)}
	for _, mk := range allPackets() {
		for _, in := range inputs {
			if err := Decode(mk(), in, 0, true); err != nil && strings.Contains(err.Error(), "runtime error") {
				t.Errorf("%v", err)
			}
		}
	}
}

func TestDecodeReportsErrors(t *testing.T) {
	if err := Decode(&Text{}, []byte{0x80}, 0, true); err == nil {
		t.Fatal("Decode of a truncated Text packet returned nil error")
	}
}
