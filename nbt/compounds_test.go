package nbt

import (
	"bytes"
	"io"
	"testing"
)

func TestEachCompoundAndDecodeWithout(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoderWithEncoding(&buf, LittleEndian)
	a := map[string]any{"x": int32(1), "Items": []any{map[string]any{"Name": "a", "tag": map[string]any{"pages": "big"}}}}
	b := map[string]any{"x": int32(2), "id": "Chest"}
	for _, m := range []map[string]any{a, b} {
		if err := enc.Encode(m); err != nil {
			t.Fatal(err)
		}
	}
	var got []map[string]any
	err := EachCompound(buf.Bytes(), LittleEndian, func(c []byte) bool {
		m, err := DecodeWithout(c, LittleEndian, func(n string) bool { return n == "tag" })
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, m)
		return true
	})
	if err != nil || len(got) != 2 || got[0]["x"] != int32(1) || got[1]["id"] != "Chest" {
		t.Fatalf("got %v, %v", got, err)
	}
	item := got[0]["Items"].([]any)[0].(map[string]any)
	if _, ok := item["tag"]; ok || item["Name"] != "a" {
		t.Fatalf("tag not skipped: %v", item)
	}
}

func TestCompoundReader(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoderWithEncoding(&buf, LittleEndian)
	for i := range 3 {
		if err := enc.Encode(map[string]any{"x": int32(i), "s": string(make([]byte, 30000))}); err != nil {
			t.Fatal(err)
		}
	}
	all := buf.Bytes()
	r := NewCompoundReader(bytes.NewReader(all), int64(len(all)), LittleEndian)
	off := 0
	for i := range 3 {
		c, err := r.Next()
		if err != nil {
			t.Fatal(i, err)
		}
		if !bytes.Equal(c, all[off:off+len(c)]) {
			t.Fatalf("compound %d differs", i)
		}
		m, err := DecodeWithout(c, LittleEndian, nil)
		if err != nil || m["x"] != int32(i) {
			t.Fatalf("compound %d: %v %v", i, m["x"], err)
		}
		off += len(c)
	}
	if _, err := r.Next(); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
	if _, err := NewCompoundReader(bytes.NewReader(all[:50]), 50, LittleEndian).Next(); err == nil {
		t.Fatal("truncated compound read without error")
	}
}
