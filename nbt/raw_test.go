package nbt

import (
	"reflect"
	"testing"
)

// TestRaw checks a Raw writes the same bytes as the value it holds, in its own encoding and in
// another, inside a compound and as list elements.
func TestRaw(t *testing.T) {
	items := []any{
		map[string]any{"Name": "minecraft:written_book", "Count": byte(1), "tag": map[string]any{"pages": []any{map[string]any{"text": "一丁"}}}},
		map[string]any{"Name": "minecraft:stone", "Count": byte(64)},
	}
	for _, enc := range []Encoding{LittleEndian, NetworkLittleEndian} {
		raw, err := MarshalRaw(items, LittleEndian)
		if err != nil {
			t.Fatal(err)
		}
		want, err := MarshalEncoding(map[string]any{"Items": items, "list": []any{items[0], items[1]}}, enc)
		if err != nil {
			t.Fatal(err)
		}
		elem, _ := MarshalRaw(items[0], LittleEndian)
		elem2, _ := MarshalRaw(items[1], LittleEndian)
		got, err := MarshalEncoding(map[string]any{"Items": raw, "list": []any{elem, elem2}}, enc)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%T: raw writes different bytes", enc)
		}
		v, err := raw.Value()
		if err != nil || !reflect.DeepEqual(v, items) {
			t.Fatalf("Value = %v, %v", v, err)
		}
	}
}

// TestRawWithout checks ValueWithout leaves out the named tags at every depth and reads the rest.
func TestRawWithout(t *testing.T) {
	book := map[string]any{"Name": "minecraft:written_book", "tag": map[string]any{"title": "t", "pages": []any{map[string]any{"text": "abc", "photoname": ""}}}}
	box := map[string]any{"Name": "minecraft:shulker_box", "tag": map[string]any{"Items": []any{book}}}
	for _, enc := range []Encoding{LittleEndian, NetworkLittleEndian} {
		raw, err := MarshalRaw([]any{box, book}, enc)
		if err != nil {
			t.Fatal(err)
		}
		v, err := raw.ValueWithout(func(name string) bool { return name == "pages" })
		if err != nil {
			t.Fatal(err)
		}
		shown := map[string]any{"Name": "minecraft:written_book", "tag": map[string]any{"title": "t"}}
		want := []any{map[string]any{"Name": "minecraft:shulker_box", "tag": map[string]any{"Items": []any{shown}}}, shown}
		if !reflect.DeepEqual(v, want) {
			t.Fatalf("%T: got %v", enc, v)
		}
	}
}
