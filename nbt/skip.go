package nbt

// skipPayload reads past the payload of a tag of type t without keeping it. Strings and byte
// arrays are passed over without being copied, so skipping big text costs no allocations.
func (d *Decoder) skipPayload(t tagType) error {
	switch t {
	case tagByte:
		if _, err := d.r.ReadByte(); err != nil {
			return BufferOverrunError{Op: "SkipByte"}
		}
	case tagInt16:
		_, err := d.Encoding.Int16(d.r)
		return err
	case tagInt32:
		_, err := d.Encoding.Int32(d.r)
		return err
	case tagInt64:
		_, err := d.Encoding.Int64(d.r)
		return err
	case tagFloat32:
		_, err := d.Encoding.Float32(d.r)
		return err
	case tagFloat64:
		_, err := d.Encoding.Float64(d.r)
		return err
	case tagString:
		return d.skipString()
	case tagByteArray:
		n, err := d.Encoding.Int32(d.r)
		if err != nil {
			return err
		}
		return d.skipBytes(int64(n), "SkipByteArray")
	case tagInt32Array:
		_, err := d.Encoding.Int32Slice(d.r)
		return err
	case tagInt64Array:
		_, err := d.Encoding.Int64Slice(d.r)
		return err
	case tagSlice:
		return d.skipList()
	case tagStruct:
		return d.skipCompound()
	default:
		return UnknownTagError{Off: d.r.off, TagType: t, Op: "Skip"}
	}
	return nil
}

// skipList reads past a list's element type, length and elements.
func (d *Decoder) skipList() error {
	d.depth++
	defer func() { d.depth-- }()
	b, err := d.r.ReadByte()
	if err != nil {
		return BufferOverrunError{Op: "SkipList"}
	}
	listType := tagType(b)
	if !listType.IsValid() {
		return UnknownTagError{Off: d.r.off, TagType: listType, Op: "SkipList"}
	}
	switch listType {
	case tagInt32:
		_, err := d.Encoding.Int32Slice(d.r)
		return err
	case tagInt64:
		_, err := d.Encoding.Int64Slice(d.r)
		return err
	}
	n, err := d.Encoding.Int32(d.r)
	if err != nil {
		return err
	}
	if n < 0 || listType == tagEnd && n != 0 {
		return BufferOverrunError{Op: "SkipList"}
	}
	if listType == tagByte {
		return d.skipBytes(int64(n), "SkipList")
	}
	if err := d.checkRemaining(int(n), "SkipList"); err != nil {
		return err
	}
	for range n {
		if err := d.skipPayload(listType); err != nil {
			return err
		}
	}
	return nil
}

// skipCompound reads past every tag of a compound and its end tag.
func (d *Decoder) skipCompound() error {
	d.depth++
	defer func() { d.depth-- }()
	for {
		t, _, err := d.tag()
		if err != nil {
			return err
		}
		if t == tagEnd {
			return nil
		}
		if err := d.skipPayload(t); err != nil {
			return err
		}
	}
}

// skipString reads past a string: a varuint32 length in network little endian, else 16 bits.
func (d *Decoder) skipString() error {
	if e, ok := d.Encoding.(networkLittleEndian); ok {
		n, err := e.stringLength(d.r)
		if err != nil {
			return err
		}
		return d.skipBytes(int64(n), "SkipString")
	}
	n, err := d.Encoding.Int16(d.r)
	if err != nil {
		return err
	}
	return d.skipBytes(int64(uint16(n)), "SkipString")
}

// skipBytes reads past n bytes.
func (d *Decoder) skipBytes(n int64, op string) error {
	if n < 0 || !d.r.fits(n) {
		return BufferOverrunError{Op: op}
	}
	if got := d.r.Next(int(n)); int64(len(got)) != n {
		return BufferOverrunError{Op: op}
	}
	return nil
}
