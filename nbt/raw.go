package nbt

import (
	"bytes"
	"errors"
	"math"
	"reflect"
)

// Raw is one tag's value kept in its encoded form. Big values that are rarely read, such as
// the items inside an item (a shulker box full of books), can be held as Raw: one byte slice
// with no pointers, about the size the value takes on disk, instead of maps and strings the
// collector scans and that take half as much again. Writing a Raw into an encoder of the same
// encoding copies its bytes; another encoding decodes it first.
//
// A Raw is never changed after it is made, so copies of it may be shared and read from any
// goroutine. Its zero value holds nothing and cannot be written.
type Raw struct {
	t    tagType
	enc  Encoding
	data []byte
}

var rawType = reflect.TypeOf(Raw{})

// errEmptyRaw is the zero Raw being written or read.
var errEmptyRaw = errors.New("nbt: empty Raw")

// MarshalRaw encodes v, which may be any value Marshal takes (a Raw is returned as it is), into
// a Raw of the encoding passed.
func MarshalRaw(v any, encoding Encoding) (Raw, error) {
	if r, ok := v.(Raw); ok {
		return r, nil
	}
	val := reflect.ValueOf(v)
	if val.Kind() == reflect.Pointer {
		val = val.Elem()
	}
	if !val.IsValid() {
		return Raw{}, IncompatibleTypeError{Type: nil}
	}
	t := tagFromType(val.Type())
	if t == math.MaxUint8 || t == tagEnd {
		return Raw{}, IncompatibleTypeError{Type: val.Type()}
	}
	b := new(bytes.Buffer)
	e := &Encoder{w: &offsetWriter{Writer: b, WriteByte: b.WriteByte}, Encoding: encoding}
	if err := e.encode(val, ""); err != nil {
		return Raw{}, err
	}
	// An exact copy: a buffer grown by doubling holds up to twice the bytes.
	return Raw{t: t, enc: encoding, data: bytes.Clone(b.Bytes())}, nil
}

// Value decodes the Raw as Unmarshal into an any would: maps for compounds, []any for lists.
// Each call decodes again and returns values nothing else holds.
func (r Raw) Value() (any, error) {
	return r.ValueWithout(nil)
}

// ValueWithout is Value with every tag of a compound, at any depth, whose name skip reports
// true for left out. Skipped strings are never copied, so leaving out big text costs no
// allocations.
func (r Raw) ValueWithout(skip func(name string) bool) (any, error) {
	if r.data == nil {
		return nil, errEmptyRaw
	}
	d := &Decoder{Encoding: r.enc, r: newOffsetReader(bytes.NewBuffer(r.data)), skip: skip}
	var v any
	if err := d.unmarshalTag(reflect.ValueOf(&v).Elem(), r.t, ""); err != nil {
		return nil, err
	}
	return v, nil
}

// Len is the size of the encoded value in bytes.
func (r Raw) Len() int {
	return len(r.data)
}

// IsZero reports whether the Raw holds nothing.
func (r Raw) IsZero() bool {
	return r.data == nil
}

// writeRaw writes the payload of a Raw, copying its bytes when the encodings match.
func (e *Encoder) writeRaw(r Raw) error {
	if r.data == nil {
		return errEmptyRaw
	}
	if r.enc == e.Encoding {
		if _, err := e.w.Write(r.data); err != nil {
			return FailedWriteError{Op: "WriteRaw", Off: e.w.off, Err: err}
		}
		return nil
	}
	v, err := r.Value()
	if err != nil {
		return err
	}
	return e.encode(reflect.ValueOf(v), "")
}

// rawTag is the tag a Raw value holds, or false when val is not a Raw.
func rawTag(val reflect.Value) (tagType, bool) {
	if !val.IsValid() || val.Type() != rawType {
		return 0, false
	}
	r, _ := val.Interface().(Raw)
	return r.t, true
}
