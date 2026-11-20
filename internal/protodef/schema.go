// Package protodef generates a ProtoDef schema (the protocol.json format used by
// PrismarineJS minecraft-data) from the Go source of the protocol package.
//
// The schema is derived by reading the Marshal methods of every packet and shared type
// as the Reader runs them: each IO call becomes a field, helpers such as Slice and
// OptionalFunc become arrays and options, and conditions on fields read earlier
// become switches. Fields a switch branches on get a mapper naming their values after
// the Go constants. Only the few encodings that cannot be followed statically are
// written by hand, in overrides.go: the items with their user-data sub-buffer, item
// descriptors (whose type is sent as a string) and the stack request action variant
// shift.
//
// The tests check the schema against the Reader and Writer for every packet: random
// values encoded with the schema must be read by the Reader with no bytes left over and
// with the same values in the Go struct, and what the Writer writes back must decode
// with the schema.
//
// Limits of the schema, all from ProtoDef having no way to say them:
//   - It accepts some values the Reader rejects: strings that must be one of a few
//     (Schema.Values lists them), numbers out of an enum's range, and events that
//     have no ordinal.
//   - A mapper leaves a value without a name as a number. node-protodef's compiler
//     (used by bedrock-protocol) does so too; its interpreter rejects such values.
//   - The item user data is an "encapsulated" type, a native of bedrock-protocol, which
//     reads length 0 as no data. Unlike the Reader, bedrock-protocol does not skip
//     bytes left over in the user data.
//   - The one Bitset (131 bits) is a varint128.
package protodef

import (
	"bytes"
	"encoding/json"
)

// Field is one field of a ProtoDef container.
type Field struct {
	// Name is the field's name, empty for an anonymous field.
	Name string `json:"name,omitempty"`
	// Anon is set for a switch whose case containers are merged into the parent
	// container, as ProtoDef's anon does.
	Anon bool `json:"anon,omitempty"`
	// Type is the field's type: a type name, or a list such as ["array", {...}].
	Type any `json:"type"`
}

// Map is a JSON object that keeps the insertion order of its keys.
type Map struct {
	keys []string
	vals map[string]any
}

// NewMap returns an empty Map.
func NewMap() *Map { return &Map{vals: map[string]any{}} }

// Set sets key to v. A key that is already set keeps its position.
func (m *Map) Set(key string, v any) {
	if _, ok := m.vals[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.vals[key] = v
}

// Get returns the value of key.
func (m *Map) Get(key string) (any, bool) {
	v, ok := m.vals[key]
	return v, ok
}

// Keys returns the keys of m in insertion order.
func (m *Map) Keys() []string { return m.keys }

// Len returns the number of keys in m.
func (m *Map) Len() int { return len(m.keys) }

// MarshalJSON writes m with its keys in insertion order.
func (m *Map) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(m.vals[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Helpers that build the ProtoDef forms used by the generator.

func container(fields []*Field) any {
	if fields == nil {
		fields = []*Field{}
	}
	return []any{"container", fields}
}

func arrayOf(countType string, typ any) any {
	m := NewMap()
	m.Set("countType", countType)
	m.Set("type", typ)
	return []any{"array", m}
}

func arrayCount(count any, typ any) any {
	m := NewMap()
	m.Set("count", count)
	m.Set("type", typ)
	return []any{"array", m}
}

func option(typ any) any { return []any{"option", typ} }

func switchOf(compareTo string, fields *Map, def any) any {
	m := NewMap()
	m.Set("compareTo", compareTo)
	m.Set("fields", fields)
	if def != nil {
		m.Set("default", def)
	}
	return []any{"switch", m}
}

func encapsulated(lengthType, typ string) any {
	m := NewMap()
	m.Set("lengthType", lengthType)
	m.Set("type", typ)
	return []any{"encapsulated", m}
}

func mapper(typ any, mappings *Map) any {
	m := NewMap()
	m.Set("type", typ)
	m.Set("mappings", mappings)
	return []any{"mapper", m}
}
