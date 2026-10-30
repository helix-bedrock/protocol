package nbt

import (
	"bytes"
	"testing"
)

func TestMapKeysSorted(t *testing.T) {
	m := map[string]any{"b": int32(2), "a": int32(1), "c": map[string]any{"z": byte(1), "y": byte(2)}}
	first, err := MarshalEncoding(m, LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	if i, j := bytes.Index(first, []byte("a")), bytes.Index(first, []byte("b")); i < 0 || i > j {
		t.Fatalf("keys not sorted: %x", first)
	}
	for range 20 {
		again, _ := MarshalEncoding(m, LittleEndian)
		if !bytes.Equal(again, first) {
			t.Fatal("encoding changes between calls")
		}
	}
}
