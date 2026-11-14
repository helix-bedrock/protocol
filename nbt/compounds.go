package nbt

import (
	"bufio"
	"bytes"
	"io"
	"reflect"
	"slices"
)

// EachCompound calls fn with the encoded bytes of each root compound stored back to back in
// data, as BDS stores a chunk's block entities, until fn returns false. The compounds are read
// past without being decoded, and each slice passed shares data's memory, so fn must copy what
// it keeps.
func EachCompound(data []byte, encoding Encoding, fn func(compound []byte) bool) error {
	d := &Decoder{Encoding: encoding, r: newOffsetReader(bytes.NewBuffer(data))}
	for int(d.r.off) < len(data) {
		start := d.r.off
		t, _, err := d.tag()
		if err != nil {
			return err
		}
		if t != tagStruct {
			return UnexpectedTagError{Off: d.r.off, TagType: t}
		}
		if err := d.skipPayload(t); err != nil {
			return err
		}
		if !fn(data[start:d.r.off]) {
			return nil
		}
	}
	return nil
}

// DecodeWithout decodes the root compound encoded in data into a map, leaving out every tag,
// at any depth, whose name skip reports true for (all are kept when skip is nil). Skipped tags
// are read past without being decoded, so leaving out big lists costs almost nothing.
func DecodeWithout(data []byte, encoding Encoding, skip func(name string) bool) (map[string]any, error) {
	d := &Decoder{Encoding: encoding, r: newOffsetReader(bytes.NewBuffer(data)), skip: skip}
	t, _, err := d.tag()
	if err != nil {
		return nil, err
	}
	if t != tagStruct {
		return nil, UnexpectedTagError{Off: d.r.off, TagType: t}
	}
	m := map[string]any{}
	if err := d.unmarshalTag(reflect.ValueOf(&m).Elem(), t, ""); err != nil {
		return nil, err
	}
	return m, nil
}

// CompoundReader reads root compounds stored back to back in a stream, one at a time, so a
// value of any size is walked holding only its biggest compound. Not safe for concurrent use.
type CompoundReader struct {
	d   *Decoder
	rec *recordingReader
}

// NewCompoundReader reads the size bytes of compounds that r holds. size bounds every length
// read, so corrupt data cannot make it allocate more than that.
func NewCompoundReader(r io.Reader, size int64, encoding Encoding) *CompoundReader {
	rec := &recordingReader{r: bufio.NewReaderSize(r, 64<<10), left: size}
	return &CompoundReader{d: &Decoder{Encoding: encoding, r: newOffsetReader(rec)}, rec: rec}
}

// Next returns the encoded bytes of the next compound, read past without being decoded, or
// io.EOF after the last. The slice is reused by the next call, so the caller copies what it
// keeps.
func (c *CompoundReader) Next() ([]byte, error) {
	if c.rec.left == 0 {
		return nil, io.EOF
	}
	c.rec.buf = c.rec.buf[:0]
	t, _, err := c.d.tag()
	if err != nil {
		return nil, err
	}
	if t != tagStruct {
		return nil, UnexpectedTagError{Off: c.d.r.off, TagType: t}
	}
	if err := c.d.skipPayload(t); err != nil {
		return nil, err
	}
	return c.rec.buf, nil
}

// recordingReader reads at most left bytes of r and keeps what it read since the last reset
// in buf.
type recordingReader struct {
	r    *bufio.Reader
	buf  []byte
	left int64
}

func (r *recordingReader) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	// Fixed-width values are read with one Read, so it fills p whole.
	n, err := io.ReadFull(r.r, p)
	r.buf = append(r.buf, p[:n]...)
	r.left -= int64(n)
	return n, err
}

func (r *recordingReader) ReadByte() (byte, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	b, err := r.r.ReadByte()
	if err != nil {
		return 0, err
	}
	r.buf = append(r.buf, b)
	r.left--
	return b, nil
}

// Next reads n bytes, or as many as are left. The slice is only valid until the next read.
func (r *recordingReader) Next(n int) []byte {
	if int64(n) > r.left {
		n = int(r.left)
	}
	start := len(r.buf)
	r.buf = slices.Grow(r.buf, n)[:start+n]
	m, _ := io.ReadFull(r.r, r.buf[start:])
	r.buf = r.buf[:start+m]
	r.left -= int64(m)
	return r.buf[start:]
}

// Len is how many bytes are left, so lengths read are checked against it.
func (r *recordingReader) Len() int { return int(min(r.left, int64(^uint(0)>>1))) }
