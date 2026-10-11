package nbt

import "testing"

// FuzzUnmarshal decodes random bytes as NBT in every encoding. It must never
// panic.
func FuzzUnmarshal(f *testing.F) {
	f.Add([]byte{10, 0, 0, 0})
	f.Add([]byte{9, 0, 0, 10, 0xff, 0xff, 0xff, 0x7f})
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, enc := range []Encoding{NetworkLittleEndian, LittleEndian, BigEndian} {
			var m map[string]any
			_ = UnmarshalEncoding(data, &m, enc)
			var a any
			_ = UnmarshalEncoding(data, &a, enc)
		}
	})
}
