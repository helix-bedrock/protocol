package protodef

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// The round trip checks bytes, which cannot tell li32 from lf32 or varint from zigzag32:
// both read the same bytes. compareGo checks values instead. For every field the Reader
// reads straight into a Go struct field (Schema.goFields), the value the schema decodes
// must equal the value the Reader stored.

// findField returns the type of the field called name in a container type, looking into
// the cases of anonymous switches too.
func findField(t any, name string) (any, bool) {
	kind, arg := typeKind(t)
	switch kind {
	case "container":
		fields, _ := arg.([]any)
		for _, f := range fields {
			fm, _ := f.(map[string]any)
			if fm["name"] == name {
				return fm["type"], true
			}
			if anon, _ := fm["anon"].(bool); anon {
				if ft, ok := findField(fm["type"], name); ok {
					return ft, true
				}
			}
		}
	case "switch":
		opts, _ := arg.(map[string]any)
		cases, _ := opts["fields"].(map[string]any)
		for _, k := range keysOf(cases) {
			if ft, ok := findField(cases[k], name); ok {
				return ft, true
			}
		}
		if d, ok := opts["default"]; ok {
			return findField(d, name)
		}
	}
	return nil, false
}

// goField follows a field path such as Stack.Count from v.
func goField(v reflect.Value, path string) (reflect.Value, bool) {
	for _, name := range strings.Split(path, ".") {
		v = deref(v)
		if v.Kind() != reflect.Struct {
			return reflect.Value{}, false
		}
		v = v.FieldByName(name)
		if !v.IsValid() {
			return reflect.Value{}, false
		}
	}
	return v, true
}

func deref(v reflect.Value) reflect.Value {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && !v.IsNil() {
		v = v.Elem()
	}
	return v
}

// intBits returns the width and signedness of an integer type.
func intBits(kind string) (int, bool, bool) {
	switch kind {
	case "u8", "byterot":
		return 8, false, true
	case "i8":
		return 8, true, true
	case "lu16":
		return 16, false, true
	case "li16":
		return 16, true, true
	case "varint", "lu32":
		return 32, false, true
	case "li32", "i32", "zigzag32":
		return 32, true, true
	case "varint64", "lu64":
		return 64, false, true
	case "li64", "zigzag64":
		return 64, true, true
	}
	return 0, false, false
}

// normalise cuts bits to the width of a type and sign-extends them if it is signed.
func normalise(bits uint64, width int, signed bool) uint64 {
	if width < 64 {
		bits &= 1<<width - 1
		if signed && bits&(1<<(width-1)) != 0 {
			bits |= ^uint64(0) << width
		}
	}
	return bits
}

type comparer struct {
	in       *interp
	goFields map[string]map[string][]string
	errs     []string
	// converted is set while comparing a value stored in another integer type.
	converted bool
}

func (c *comparer) fail(path string, format string, args ...any) {
	c.errs = append(c.errs, path+": "+fmt.Sprintf(format, args...))
}

// object compares the fields of a value of the named container type with a Go value.
func (c *comparer) object(typeName string, o *obj, gv reflect.Value, path string) {
	def, ok := c.in.lookup(typeName)
	if !ok {
		return
	}
	fields := c.goFields[typeName]
	for _, name := range keysOf(boolMapOf(fields)) {
		sv, present := o.vals[name]
		if !present {
			continue
		}
		ft, ok := findField(def, name)
		if !ok {
			c.fail(path+"."+name, "field not in type %s", typeName)
			continue
		}
		// A field read in several switch cases goes to a Go field per case: one must match.
		var firstErrs []string
		matched := false
		for _, goPath := range fields[name] {
			goPath, converted := strings.CutPrefix(goPath, "~")
			fv, ok := goField(gv, goPath)
			if !ok {
				c.fail(path+"."+name, "Go value has no field %s", goPath)
				matched = true
				break
			}
			sub := &comparer{in: c.in, goFields: c.goFields, converted: converted}
			sub.value(ft, sv, fv, path+"."+name)
			if len(sub.errs) == 0 {
				matched = true
				break
			}
			if firstErrs == nil {
				firstErrs = sub.errs
			}
		}
		if !matched {
			c.errs = append(c.errs, firstErrs...)
		}
	}
}

func boolMapOf(m map[string][]string) map[string]any {
	out := make(map[string]any, len(m))
	for k := range m {
		out[k] = nil
	}
	return out
}

// value compares one decoded value of type t with the Go value gv.
func (c *comparer) value(t any, sv any, gv reflect.Value, path string) {
	gv = deref(gv)
	if !gv.IsValid() {
		return
	}
	kind, arg := typeKind(t)
	if width, signed, ok := intBits(kind); ok && kind != "byterot" {
		n, isInt := sv.(int64)
		if !isInt {
			c.fail(path, "schema value %v is not an integer", sv)
			return
		}
		var bits uint64
		exact := true
		switch gv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			bits = uint64(gv.Int())
			exact = gv.Int() == n
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			bits = gv.Uint()
			// The 64-bit unsigned types are kept in an int64.
			exact = bits == uint64(n) && (n >= 0 || width == 64 && !signed)
		case reflect.Bool:
			if gv.Bool() {
				bits = 1
			}
		case reflect.Float32, reflect.Float64, reflect.String, reflect.Slice:
			c.fail(path, "%s read into a Go %s", kind, gv.Type())
			return
		default:
			// A value the Reader converts, such as an RGBA colour from a uint32.
			return
		}
		// A value read straight into a Go integer has the wire type's sign: compare it as a
		// number. One stored in another type (IntegerFunc) is compared by its bits.
		if c.converted && normalise(bits, width, signed) != normalise(uint64(n), width, signed) ||
			!c.converted && gv.Kind() != reflect.Bool && !exact {
			c.fail(path, "schema %s decodes %d, the Reader stored %v", kind, n, gv)
		}
		if gv.Kind() == reflect.Bool && normalise(bits, width, signed) != normalise(uint64(n), width, signed) {
			c.fail(path, "schema %s decodes %d, the Reader stored %v", kind, n, gv)
		}
		return
	}
	switch kind {
	case "bool":
		b, _ := sv.(bool)
		if gv.Kind() != reflect.Bool {
			c.fail(path, "bool read into a Go %s", gv.Type())
		} else if gv.Bool() != b {
			c.fail(path, "schema decodes %v, the Reader stored %v", b, gv.Bool())
		}
	case "lf32", "lf64":
		f, _ := sv.(float64)
		if gv.Kind() != reflect.Float32 && gv.Kind() != reflect.Float64 {
			c.fail(path, "%s read into a Go %s", kind, gv.Type())
			return
		}
		g := gv.Float()
		if kind == "lf32" {
			if math.Float32bits(float32(f)) != math.Float32bits(float32(g)) {
				c.fail(path, "schema decodes %v, the Reader stored %v", f, g)
			}
		} else if math.Float64bits(f) != math.Float64bits(g) {
			c.fail(path, "schema decodes %v, the Reader stored %v", f, g)
		}
	case "pstring", "buffer":
		var want string
		switch v := sv.(type) {
		case string:
			want = v
		case []byte:
			want = string(v)
		}
		switch {
		case gv.Kind() == reflect.String:
			if gv.String() != want {
				c.fail(path, "schema decodes %q, the Reader stored %q", want, gv.String())
			}
		case gv.Kind() == reflect.Slice && gv.Type().Elem().Kind() == reflect.Uint8:
			if string(gv.Bytes()) != want {
				c.fail(path, "schema decodes %q, the Reader stored %q", want, gv.Bytes())
			}
		default:
			c.fail(path, "%s read into a Go %s", kind, gv.Type())
		}
	case "mapper":
		opts, _ := arg.(map[string]any)
		if name, ok := sv.(string); ok {
			for k, n := range opts["mappings"].(map[string]any) {
				if n == name {
					v, err := strconv.ParseInt(k, 10, 64)
					if err == nil {
						sv = v
					}
				}
			}
		}
		c.value(opts["type"], sv, gv, path)
	case "option":
		// protocol.Optional keeps whether it is set and the value in unexported fields.
		set, val := gv.FieldByName("set"), gv.FieldByName("val")
		if gv.Kind() != reflect.Struct || !set.IsValid() || !val.IsValid() {
			return
		}
		if sv == nil {
			if set.Bool() {
				c.fail(path, "schema decodes no value, the Reader stored one")
			}
			return
		}
		if !set.Bool() {
			c.fail(path, "schema decodes a value, the Reader stored none")
			return
		}
		c.value(arg, sv, val, path)
	case "switch":
		// A field whose type depends on another: one of the types must give the value.
		opts, _ := arg.(map[string]any)
		cases, _ := opts["fields"].(map[string]any)
		candidates := make([]any, 0, len(cases)+1)
		for _, k := range keysOf(cases) {
			candidates = append(candidates, cases[k])
		}
		if d, ok := opts["default"]; ok {
			candidates = append(candidates, d)
		}
		var first []string
		for _, ct := range candidates {
			if ct == "void" {
				// void would match any value without comparing it.
				continue
			}
			sub := &comparer{in: c.in, goFields: c.goFields, converted: c.converted}
			sub.value(ct, sv, gv, path)
			if len(sub.errs) == 0 {
				return
			}
			if first == nil {
				first = sub.errs
			}
		}
		c.errs = append(c.errs, first...)
	case "array":
		opts, _ := arg.(map[string]any)
		items, _ := sv.([]any)
		if gv.Kind() != reflect.Slice && gv.Kind() != reflect.Array {
			return
		}
		if gv.Len() != len(items) {
			c.fail(path, "schema decodes %d elements, the Reader stored %d", len(items), gv.Len())
			return
		}
		for i, it := range items {
			c.value(opts["type"], it, gv.Index(i), fmt.Sprintf("%s[%d]", path, i))
		}
	default:
		def, ok := c.in.lookup(kind)
		if !ok || arg != nil {
			return
		}
		if o, isObj := sv.(*obj); isObj {
			if _, has := c.goFields[kind]; has && gv.Kind() == reflect.Struct {
				c.object(kind, o, gv, path)
			}
			return
		}
		// An alias such as string or ByteArray.
		c.value(def, sv, gv, path)
	}
}
