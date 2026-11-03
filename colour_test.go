package protocol

import (
	"bytes"
	"image/color"
	"testing"
)

// TestARGBBytes pins a colour's bytes to BDS's little endian ARGB.
func TestARGBBytes(t *testing.T) {
	in := color.RGBA{R: 1, G: 2, B: 3, A: 4}
	var buf bytes.Buffer
	NewWriter(&buf, 0).ARGB(&in)
	if want := []byte{3, 2, 1, 4}; !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("wrote % x, want % x", buf.Bytes(), want)
	}
	var out color.RGBA
	NewReader(&buf, 0, false).ARGB(&out)
	if out != in {
		t.Fatalf("read %v, want %v", out, in)
	}
}
