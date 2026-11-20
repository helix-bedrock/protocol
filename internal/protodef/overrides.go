package protodef

import (
	"go/types"
	"strconv"
)

// overrides are the IO methods whose Reader code cannot be followed statically, written
// by hand from reader.go. The test in this package decodes what the Writer writes with
// them, so they are checked like the generated types.
func overrides() map[string]func(g *gen) any {
	return map[string]func(g *gen) any{
		// The item encodings end in a length-prefixed user-data blob, which the Reader reads
		// with a second Reader over the blob, and whose last field is only there for the
		// shield item (a network ID only known at runtime, /ShieldItemID in bedrock-protocol).
		"Item": func(g *gen) any {
			return container([]*Field{
				{Name: "network_id", Type: "zigzag32"},
				{Name: "count", Type: "lu16"},
				{Name: "metadata", Type: "varint"},
				{Name: "block_runtime_id", Type: "zigzag32"},
				{Name: "extra", Type: g.itemExtra("network_id", "/ShieldItemID")},
			})
		},
		"ItemInstance": func(g *gen) any {
			return container([]*Field{
				{Name: "network_id", Type: "li16"},
				{Name: "count", Type: "lu16"},
				{Name: "metadata", Type: "varint"},
				{Name: "has_stack_id", Type: "bool"},
				{Name: "stack_id", Type: switchOf("has_stack_id", mapOf("true", "zigzag32"), "void")},
				{Name: "block_runtime_id", Type: "varint"},
				{Name: "extra", Type: g.itemExtra("network_id", "/ShieldItemID")},
			})
		},
		// StackRequestItem names its item, so the shield is found by its identifier.
		// Variant 0 is no item: it has no identifier, and its user data is empty. The rest of
		// the item is in each case, so that the shield switch only names a field of its own
		// container.
		"StackRequestItem": func(g *gen) any {
			return container([]*Field{
				{Name: "variant", Type: "varint"},
				{Name: "legacy_variant", Type: "u8"},
				{Anon: true, Type: switchOf("variant", mapOf(
					"0", container([]*Field{
						{Name: "count", Type: "li16"},
						{Name: "block_runtime_id", Type: "varint"},
						{Name: "extra", Type: encapsulated("varint", g.itemUserData("ItemExtraDataWithoutBlockingTick", false))},
					}),
					"1", container([]*Field{
						{Name: "identifier", Type: "string"},
						{Name: "metadata", Type: "zigzag32"},
						{Name: "count", Type: "li16"},
						{Name: "block_runtime_id", Type: "varint"},
						{Name: "extra", Type: g.itemExtra("identifier", "minecraft:shield")},
					}),
				), nil)},
			})
		},
		// The descriptor type is sent as a string, then turned into an ID that picks the type.
		"ItemDescriptorCount": func(g *gen) any {
			return container([]*Field{
				{Name: "variant", Type: "varint"},
				{Anon: true, Type: switchOf("variant", mapOf(
					"0", container([]*Field{{Name: "aux", Type: "zigzag32"}}),
					"1", container([]*Field{
						{Name: "descriptor_type", Type: "string"},
						{Name: "descriptor", Type: switchOf("descriptor_type", mapOf(
							"name", g.rootMarshaler("DefaultItemDescriptor"),
							"molang", g.rootMarshaler("MoLangItemDescriptor"),
							"item_tag", g.rootMarshaler("ItemTagItemDescriptor"),
						), nil)},
						{Name: "aux", Type: switchOf("descriptor_type", mapOf("item_tag", "zigzag32"), "void")},
					}),
				), nil)},
				{Name: "count", Type: "zigzag32"},
			})
		},
		// The variant skips the IDs of the PlaceInContainer and TakeOutContainer actions, which
		// are never sent: variant 7 and up is action ID variant+2.
		"StackRequestAction": func(g *gen) any {
			fields := NewMap()
			fn, _ := g.p.root.Scope().Lookup("lookupStackRequestAction").(*types.Func)
			placeIn := g.rootConst("StackRequestActionPlaceInContainer")
			if t := g.lookupTable(fn); t != nil && placeIn >= 0 {
				for _, k := range t.keys {
					id, err := strconv.Atoi(k)
					if err != nil {
						continue
					}
					variant := id
					if id >= placeIn+2 {
						variant = id - 2
					} else if id >= placeIn {
						continue
					}
					fields.Set(strconv.Itoa(variant), g.topMarshalerType(t.types[k]))
				}
			} else {
				g.diags = append(g.diags, "protodef: lookupStackRequestAction or StackRequestActionPlaceInContainer not found")
			}
			return container([]*Field{
				{Name: "variant", Type: "varint"},
				{Name: "legacy_id", Type: "u8"},
				{Name: "action", Type: switchOf("variant", fields, nil)},
			})
		},
	}
}

// itemExtra is the user-data blob of an item, with the blocking tick when the field at
// ref is the shield.
func (g *gen) itemExtra(ref, shield string) any {
	g.itemUserData("ItemExtraDataWithBlockingTick", true)
	g.itemUserData("ItemExtraDataWithoutBlockingTick", false)
	return switchOf(ref,
		mapOf(shield, encapsulated("varint", "ItemExtraDataWithBlockingTick")),
		encapsulated("varint", "ItemExtraDataWithoutBlockingTick"))
}

func (g *gen) itemUserData(name string, shield bool) string {
	return g.fixedType("override:"+name, name, func() any {
		fields := []*Field{
			{Name: "nbt_marker", Type: "li16"},
			{Name: "nbt", Type: switchOf("nbt_marker", mapOf(
				"0", "void",
				"-1", container([]*Field{{Name: "version", Type: "u8"}, {Name: "nbt", Type: "lnbt"}}),
			), "lnbt")},
			{Name: "can_place_on", Type: arrayOf("lu32", "ShortString")},
			{Name: "can_destroy", Type: arrayOf("lu32", "ShortString")},
		}
		if shield {
			fields = append(fields, &Field{Name: "blocking_tick", Type: "li64"})
		}
		return container(fields)
	})
}

func (g *gen) rootMarshaler(name string) any {
	o := g.p.root.Scope().Lookup(name)
	if o == nil {
		g.diags = append(g.diags, "protodef: no type "+name)
		return "void"
	}
	return g.topMarshalerType(o.Type())
}

func (g *gen) rootConst(name string) int {
	c, ok := g.p.root.Scope().Lookup(name).(*types.Const)
	if !ok {
		return -1
	}
	n, err := strconv.Atoi(c.Val().ExactString())
	if err != nil {
		return -1
	}
	return n
}

// mapOf makes a Map from keys and values in turn: mapOf("a", 1, "b", 2).
func mapOf(kv ...any) *Map {
	m := NewMap()
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			m.Set(k, kv[i+1])
		}
	}
	return m
}
