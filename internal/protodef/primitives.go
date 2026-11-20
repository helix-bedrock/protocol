package protodef

// ioPrimitive returns the ProtoDef type of an IO method with a fixed encoding. The types
// follow the reader: Varint32 is a zigzag varint, Varuint32 a plain one, and every
// fixed-width integer and float is little-endian except BEInt32.
func ioPrimitive(name string) (any, bool) {
	switch name {
	case "Uint8":
		return "u8", true
	case "Int8":
		return "i8", true
	case "Bool":
		return "bool", true
	case "Uint16":
		return "lu16", true
	case "Int16":
		return "li16", true
	case "Uint32", "RGBA", "ARGB":
		return "lu32", true
	case "Int32":
		return "li32", true
	case "BEInt32":
		return "i32", true
	case "Uint64", "ActorUniqueIDUint64":
		return "lu64", true
	case "Int64", "ActorUniqueIDInt64":
		return "li64", true
	case "Float32":
		return "lf32", true
	case "Float64":
		return "lf64", true
	case "Varuint32", "ActorRuntimeIDVaruint32":
		return "varint", true
	case "Varint32":
		return "zigzag32", true
	case "Varuint64", "ActorRuntimeID", "ActorUniqueIDVaruint64":
		return "varint64", true
	case "Varint64", "ActorRuntimeIDVarint64", "ActorUniqueID":
		return "zigzag64", true
	case "String":
		return "string", true
	case "StringUTF":
		return "ShortString", true
	case "ByteSlice":
		return "ByteArray", true
	case "Bytes":
		return "restBuffer", true
	case "Vec3":
		return "vec3f", true
	case "Vec2":
		return "vec2f", true
	case "BlockPos":
		return "BlockCoordinates", true
	// SoundPos is a BlockPos holding the position times 8.
	case "SoundPos":
		return "SoundPosition", true
	case "ChunkPos":
		return "ChunkCoordinates", true
	case "SubChunkPos":
		return "vec3li", true
	// ByteFloat is an angle in 1/256ths of a turn.
	case "ByteFloat":
		return "byterot", true
	case "RGB":
		return "ColourRGBf", true
	case "UUID":
		return "uuid", true
	// Bitset is an unsigned LEB128 integer of any width. Every Bitset in the protocol
	// is at most 131 bits wide, which varint128 reads as long as the top bits are unset.
	case "Bitset":
		return "varint128", true
	}
	return nil, false
}

// isIOIgnored reports whether an IO or Reader method checks or reports but reads nothing.
func isIOIgnored(name string) bool {
	switch name {
	case "Err", "ShieldID", "UnknownEnumOption", "InvalidValue", "SliceLength", "checkRemaining", "fail", "failf":
		return true
	}
	return false
}

// isIOError reports whether an IO method marks a branch as only reached on invalid data.
func isIOError(name string) bool {
	switch name {
	case "UnknownEnumOption", "InvalidValue", "fail", "failf":
		return true
	}
	return false
}

// ioInline reports whether a composite IO method reads only part of a value (the type of
// a polymorphic value, or a number derived from it), so its fields belong to the
// container of the caller instead of a type of their own. It returns the name of the
// field the method reads.
func ioInline(name string) (string, bool) {
	switch name {
	case "EventType":
		return "event_type", true
	case "EventOrdinal":
		return "event_ordinal", true
	case "TransactionDataType":
		return "transaction_type", true
	}
	return "", false
}

// declaredNatives are the custom types a ProtoDef implementation has to provide. They
// are declared "native" in the schema as minecraft-data does.
func declaredNatives() []string {
	return []string{"varint64", "zigzag32", "zigzag64", "uuid", "byterot", "restBuffer", "encapsulated", "nbt", "lnbt"}
}

// aliases are the named composite types the primitives above refer to.
func aliases() *Map {
	m := NewMap()
	m.Set("string", []any{"pstring", map[string]any{"countType": "varint"}})
	m.Set("ShortString", []any{"pstring", map[string]any{"countType": "li16"}})
	m.Set("ByteArray", []any{"buffer", map[string]any{"countType": "varint"}})
	m.Set("vec3f", container([]*Field{{Name: "x", Type: "lf32"}, {Name: "y", Type: "lf32"}, {Name: "z", Type: "lf32"}}))
	m.Set("vec2f", container([]*Field{{Name: "x", Type: "lf32"}, {Name: "y", Type: "lf32"}}))
	m.Set("BlockCoordinates", container([]*Field{{Name: "x", Type: "zigzag32"}, {Name: "y", Type: "zigzag32"}, {Name: "z", Type: "zigzag32"}}))
	m.Set("SoundPosition", container([]*Field{{Name: "x", Type: "zigzag32"}, {Name: "y", Type: "zigzag32"}, {Name: "z", Type: "zigzag32"}}))
	m.Set("ChunkCoordinates", container([]*Field{{Name: "x", Type: "zigzag32"}, {Name: "z", Type: "zigzag32"}}))
	m.Set("vec3li", container([]*Field{{Name: "x", Type: "li32"}, {Name: "y", Type: "li32"}, {Name: "z", Type: "li32"}}))
	m.Set("ColourRGBf", container([]*Field{{Name: "r", Type: "lf32"}, {Name: "g", Type: "lf32"}, {Name: "b", Type: "lf32"}}))
	return m
}
