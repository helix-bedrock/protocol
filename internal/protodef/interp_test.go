package protodef

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/helix-bedrock/protocol/nbt"
)

// This file is a small ProtoDef implementation used to check the generated schema: a
// decoder with the semantics of node-protodef's interpreter (a bare name in compareTo
// or count is a field of the immediate container, "../" goes up one container, and an
// anonymous switch merges its case into the parent), an encoder, and a generator of
// random values that fit a type.

// obj is the value of a container.
type obj struct {
	vals   map[string]any
	parent *obj
	// typ is the name of the type the decoder read the container as, if it has one.
	typ string
}

func newObj(parent *obj) *obj { return &obj{vals: map[string]any{}, parent: parent} }

// resolve finds the value at a compareTo or count path, returning the container that
// holds it and its name.
func resolve(path string, ctx *obj) (*obj, string, error) {
	parts := strings.Split(path, "/")
	for len(parts) > 1 {
		if parts[0] != ".." {
			return nil, "", fmt.Errorf("unsupported path %q", path)
		}
		if ctx.parent == nil {
			return nil, "", fmt.Errorf("path %q goes above the root", path)
		}
		ctx = ctx.parent
		parts = parts[1:]
	}
	if _, ok := ctx.vals[parts[0]]; !ok {
		return nil, "", fmt.Errorf("path %q: no field %q in the container (has %v)", path, parts[0], keysOf(ctx.vals))
	}
	return ctx, parts[0], nil
}

func keysOf(m map[string]any) []string {
	k := make([]string, 0, len(m))
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}

func keyOf(v any) string {
	switch v := v.(type) {
	case nil:
		return "<nil>"
	case bool:
		return strconv.FormatBool(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case string:
		return v
	}
	return fmt.Sprint(v)
}

// caseKey returns the key of the case a switch picks for v, or "default".
func caseKey(opts map[string]any, v any, vars map[string]any) string {
	fields, _ := opts["fields"].(map[string]any)
	k := keyOf(v)
	if _, ok := fields[k]; ok {
		return k
	}
	for name := range fields {
		if strings.HasPrefix(name, "/") && keyOf(vars[name[1:]]) == k {
			return name
		}
	}
	return "default"
}

// switchCase returns the type a switch picks for value v, or false if it picks none.
func switchCase(opts map[string]any, v any, vars map[string]any) (any, bool) {
	fields, _ := opts["fields"].(map[string]any)
	k := keyOf(v)
	if t, ok := fields[k]; ok {
		return t, true
	}
	for name, t := range fields {
		if strings.HasPrefix(name, "/") && keyOf(vars[name[1:]]) == k {
			return t, true
		}
	}
	if def, ok := opts["default"]; ok {
		return def, true
	}
	return nil, false
}

type interp struct {
	types map[string]any
	vars  map[string]any
}

func (in *interp) lookup(name string) (any, bool) {
	t, ok := in.types[name]
	if !ok || t == "native" {
		return nil, false
	}
	return t, true
}

func typeKind(t any) (string, any) {
	switch t := t.(type) {
	case string:
		return t, nil
	case []any:
		if len(t) == 2 {
			if k, ok := t[0].(string); ok {
				return k, t[1]
			}
		}
	}
	return "", nil
}

// ---- decoding ----

type decoder struct {
	*interp
	buf []byte
	off int
	// taken, if set, records each switch case decoded, by switch and case key.
	taken map[string]map[string]bool
}

var errShort = errors.New("unexpected end of data")

// The sites of a schema: each switch, option and array, identified by its value in the
// decoded JSON (which every test shares), for the coverage of the round trip.
func switchSite(opts map[string]any) string { return fmt.Sprintf("S%p", opts) }
func arraySite(opts map[string]any) string  { return fmt.Sprintf("A%p", opts) }
func optionSite(t any) string {
	s, _ := t.([]any)
	return fmt.Sprintf("O%p", s)
}

// reach records that a sample reached branch of site.
func (d *decoder) reach(site, branch string) {
	if d.taken == nil {
		return
	}
	if d.taken[site] == nil {
		d.taken[site] = map[string]bool{}
	}
	d.taken[site][branch] = true
}

func (d *decoder) take(n int) ([]byte, error) {
	if n < 0 || d.off+n > len(d.buf) {
		return nil, errShort
	}
	b := d.buf[d.off : d.off+n]
	d.off += n
	return b, nil
}

func (d *decoder) uvarint(maxBits int) (uint64, error) {
	var v uint64
	for shift := 0; shift < maxBits+7; shift += 7 {
		b, err := d.take(1)
		if err != nil {
			return 0, err
		}
		if shift < 64 {
			v |= uint64(b[0]&0x7f) << shift
		}
		if b[0]&0x80 == 0 {
			return v, nil
		}
	}
	return 0, errors.New("varint too long")
}

func (d *decoder) count(opts map[string]any, ctx *obj) (int, error) {
	if ct, ok := opts["countType"]; ok {
		v, err := d.read(ct, ctx)
		if err != nil {
			return 0, err
		}
		n, ok := v.(int64)
		if !ok || n < 0 || n > 1<<20 {
			return 0, fmt.Errorf("bad count %v", v)
		}
		return int(n), nil
	}
	switch c := opts["count"].(type) {
	case float64:
		return int(c), nil
	case string:
		holder, name, err := resolve(c, ctx)
		if err != nil {
			return 0, err
		}
		n, ok := holder.vals[name].(int64)
		if !ok || n < 0 || n > 1<<20 {
			return 0, fmt.Errorf("bad count %v", holder.vals[name])
		}
		return int(n), nil
	}
	return 0, fmt.Errorf("array or string without a count: %v", opts)
}

func (d *decoder) read(t any, ctx *obj) (any, error) {
	kind, arg := typeKind(t)
	switch kind {
	case "void":
		return nil, nil
	case "bool", "u8", "i8", "byterot":
		b, err := d.take(1)
		if err != nil {
			return nil, err
		}
		switch kind {
		case "bool":
			return b[0] != 0, nil
		case "i8":
			return int64(int8(b[0])), nil
		}
		return int64(b[0]), nil
	case "li16", "lu16":
		b, err := d.take(2)
		if err != nil {
			return nil, err
		}
		u := binary.LittleEndian.Uint16(b)
		if kind == "li16" {
			return int64(int16(u)), nil
		}
		return int64(u), nil
	case "li32", "lu32", "i32", "lf32":
		b, err := d.take(4)
		if err != nil {
			return nil, err
		}
		var u uint32
		if kind == "i32" {
			u = binary.BigEndian.Uint32(b)
		} else {
			u = binary.LittleEndian.Uint32(b)
		}
		switch kind {
		case "lf32":
			return float64(math.Float32frombits(u)), nil
		case "lu32":
			return int64(u), nil
		}
		return int64(int32(u)), nil
	case "li64", "lu64", "lf64":
		b, err := d.take(8)
		if err != nil {
			return nil, err
		}
		u := binary.LittleEndian.Uint64(b)
		if kind == "lf64" {
			return math.Float64frombits(u), nil
		}
		return int64(u), nil
	case "varint":
		v, err := d.uvarint(32)
		return int64(uint32(v)), err
	case "varint64":
		v, err := d.uvarint(64)
		return int64(v), err
	case "varint128":
		v, err := d.uvarint(128)
		return int64(v), err
	case "zigzag32":
		v, err := d.uvarint(32)
		u := uint32(v)
		return int64(int32(u>>1) ^ -int32(u&1)), err
	case "zigzag64":
		v, err := d.uvarint(64)
		return int64(v>>1) ^ -int64(v&1), err
	case "uuid":
		b, err := d.take(16)
		return slices.Clone(b), err
	case "restBuffer":
		b, _ := d.take(len(d.buf) - d.off)
		return slices.Clone(b), nil
	case "nbt", "lnbt":
		var enc nbt.Encoding = nbt.NetworkLittleEndian
		if kind == "lnbt" {
			enc = nbt.LittleEndian
		}
		r := bytes.NewReader(d.buf[d.off:])
		dec := nbt.NewDecoderWithEncoding(r, enc)
		dec.AllowZero = true
		var m any
		if err := dec.Decode(&m); err != nil {
			return nil, fmt.Errorf("nbt: %w", err)
		}
		d.off = len(d.buf) - r.Len()
		return m, nil
	case "pstring", "buffer":
		opts, _ := arg.(map[string]any)
		if rest, _ := opts["rest"].(bool); rest {
			b, _ := d.take(len(d.buf) - d.off)
			return slices.Clone(b), nil
		}
		n, err := d.count(opts, ctx)
		if err != nil {
			return nil, err
		}
		b, err := d.take(n)
		if err != nil {
			return nil, err
		}
		if kind == "pstring" {
			return string(b), nil
		}
		return slices.Clone(b), nil
	case "container":
		fields, _ := arg.([]any)
		o := newObj(ctx)
		for _, f := range fields {
			fm := f.(map[string]any)
			v, err := d.read(fm["type"], o)
			if err != nil {
				return nil, fmt.Errorf("%v: %w", fieldLabel(fm), err)
			}
			if anon, _ := fm["anon"].(bool); anon {
				if sub, ok := v.(*obj); ok {
					for k, x := range sub.vals {
						o.vals[k] = x
					}
				}
				continue
			}
			o.vals[fm["name"].(string)] = v
		}
		return o, nil
	case "array":
		opts := arg.(map[string]any)
		n, err := d.count(opts, ctx)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			d.reach(arraySite(opts), "nonempty")
		}
		out := make([]any, 0, min(n, 64))
		for i := 0; i < n; i++ {
			v, err := d.read(opts["type"], ctx)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out = append(out, v)
		}
		return out, nil
	case "option":
		b, err := d.take(1)
		if err != nil {
			return nil, err
		}
		if b[0] == 0 {
			return nil, nil
		}
		d.reach(optionSite(t), "present")
		return d.read(arg, ctx)
	case "switch":
		opts := arg.(map[string]any)
		holder, name, err := resolve(opts["compareTo"].(string), ctx)
		if err != nil {
			return nil, err
		}
		ct, ok := switchCase(opts, holder.vals[name], d.vars)
		if !ok {
			return nil, fmt.Errorf("switch on %s: no case for %v", opts["compareTo"], holder.vals[name])
		}
		d.reach(switchSite(opts), caseKey(opts, holder.vals[name], d.vars))
		return d.read(ct, ctx)
	case "mapper":
		opts := arg.(map[string]any)
		v, err := d.read(opts["type"], ctx)
		if err != nil {
			return nil, err
		}
		if name, ok := opts["mappings"].(map[string]any)[keyOf(v)]; ok {
			return name, nil
		}
		return v, nil
	case "encapsulated":
		opts := arg.(map[string]any)
		lv, err := d.read(opts["lengthType"], ctx)
		if err != nil {
			return nil, err
		}
		n, _ := lv.(int64)
		if n == 0 {
			return nil, nil
		}
		b, err := d.take(int(n))
		if err != nil {
			return nil, err
		}
		sub := &decoder{interp: d.interp, buf: b, taken: d.taken}
		v, err := sub.read(opts["type"], ctx)
		if err == nil && sub.off != len(b) {
			err = fmt.Errorf("encapsulated: %d of %d bytes left", len(b)-sub.off, len(b))
		}
		return v, err
	}
	if def, ok := d.lookup(kind); ok && arg == nil {
		v, err := d.read(def, ctx)
		if o, isObj := v.(*obj); isObj && o.typ == "" {
			o.typ = kind
		}
		return v, err
	}
	return nil, fmt.Errorf("unknown type %v", t)
}

func fieldLabel(fm map[string]any) string {
	if n, ok := fm["name"].(string); ok {
		return n
	}
	return "(anon)"
}

// ---- generating ----

type generator struct {
	*interp
	rng *rand.Rand
	// fixed holds the fields a switch or count has already depended on.
	fixed map[*obj]map[string]bool
	// conflict is set when a value could not be made to fit, so the sample is dropped.
	conflict bool
	depth    int
	// nodes counts the values made, so that deep types still get options and arrays
	// while a sample stays small.
	nodes int
	// values holds the values the Reader accepts for some fields, by type and field
	// name; stack and field say where the generator is.
	values map[string]map[string][]string
	stack  []string
	field  string
}

// small returns a number that is often 0 and mostly below 8, as enums and sizes are.
func (g *generator) small() int64 {
	switch n := g.rng.IntN(10); {
	case n < 3:
		return 0
	case n < 9:
		return int64(g.rng.IntN(8))
	}
	return int64(g.rng.IntN(1 << 15))
}

// accepted returns one of the values the Reader accepts for the field being made, if
// only some are.
func (g *generator) accepted() (string, bool) {
	if len(g.stack) == 0 {
		return "", false
	}
	vals := g.values[g.stack[len(g.stack)-1]][g.field]
	if len(vals) == 0 {
		return "", false
	}
	return vals[g.rng.IntN(len(vals))], true
}

// integer returns a value of an integer type: mostly small, sometimes the extremes of
// the type, so that a schema with the wrong width, sign or encoding writes other bytes.
func (g *generator) integer(kind string) int64 {
	if g.rng.IntN(6) != 0 {
		return g.small()
	}
	lo, hi := intRange(kind)
	switch g.rng.IntN(3) {
	case 0:
		return hi
	case 1:
		return lo
	}
	if lo < 0 && hi-lo < 0 {
		// The whole int64 range: hi-lo overflows.
		return int64(g.rng.Uint64())
	}
	return lo + g.rng.Int64N(hi-lo)
}

// intRange returns the smallest and largest value of an integer type, kept within
// int64 for the 64-bit unsigned types.
func intRange(kind string) (int64, int64) {
	switch kind {
	case "u8", "byterot":
		return 0, math.MaxUint8
	case "i8":
		return math.MinInt8, math.MaxInt8
	case "lu16":
		return 0, math.MaxUint16
	case "li16":
		return math.MinInt16, math.MaxInt16
	case "varint", "lu32":
		return 0, math.MaxUint32
	case "li32", "i32", "zigzag32":
		return math.MinInt32, math.MaxInt32
	case "varint128":
		return 0, math.MaxInt32
	case "varint64", "lu64":
		return 0, math.MaxInt64
	}
	return math.MinInt64 + 1, math.MaxInt64
}

func (g *generator) fix(o *obj, name string) {
	if g.fixed[o] == nil {
		g.fixed[o] = map[string]bool{}
	}
	g.fixed[o][name] = true
}

func (g *generator) isFixed(o *obj, name string) bool { return g.fixed[o][name] }

// setCount makes the field at a count path hold n.
func (g *generator) setCount(path string, n int, ctx *obj) {
	holder, name, err := resolve(path, ctx)
	if err != nil {
		g.conflict = true
		return
	}
	if g.isFixed(holder, name) && holder.vals[name] != int64(n) {
		g.conflict = true
		return
	}
	holder.vals[name] = int64(n)
	g.fix(holder, name)
}

func (g *generator) length(opts map[string]any, ctx *obj) int {
	if c, ok := opts["count"]; ok {
		switch c := c.(type) {
		case float64:
			return int(c)
		case string:
			holder, name, err := resolve(c, ctx)
			if err != nil {
				g.conflict = true
				return 0
			}
			if g.isFixed(holder, name) {
				n, _ := holder.vals[name].(int64)
				return int(n)
			}
			n := g.rng.IntN(4)
			g.setCount(c, n, ctx)
			return n
		}
	}
	if g.full() || g.rng.IntN(2) == 0 {
		return 0
	}
	return 1 + g.rng.IntN(3)
}

// full reports whether the sample is big enough: options are left out and arrays empty
// from then on. The depth limit stops recursive types.
func (g *generator) full() bool { return g.nodes > 3000 || g.depth > 24 }

func (g *generator) gen(t any, ctx *obj) any {
	g.nodes++
	kind, arg := typeKind(t)
	switch kind {
	case "void":
		return nil
	case "bool":
		return g.rng.IntN(2) == 0
	case "u8", "byterot", "varint", "varint64", "varint128", "lu16", "lu32", "lu64", "li16", "li32", "i32", "li64", "zigzag32", "zigzag64", "i8":
		if v, ok := g.accepted(); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				return n
			}
		}
		return g.integer(kind)
	case "lf32", "lf64":
		if g.rng.IntN(4) == 0 {
			return -float64(g.rng.IntN(1000)) / 7
		}
		return float64(g.rng.IntN(64)) / 4
	case "uuid":
		b := make([]byte, 16)
		for i := range b {
			b[i] = byte(g.rng.IntN(256))
		}
		return b
	case "restBuffer":
		b := make([]byte, g.rng.IntN(5))
		for i := range b {
			b[i] = byte(g.rng.IntN(256))
		}
		return b
	case "nbt", "lnbt":
		return map[string]any{"v": int32(g.rng.IntN(100))}
	case "pstring":
		opts, _ := arg.(map[string]any)
		if v, ok := g.accepted(); ok {
			return v
		}
		n := 1 + g.rng.IntN(6)
		if c, ok := opts["count"]; ok {
			_ = c
			n = g.length(opts, ctx)
		}
		var b strings.Builder
		for range n {
			b.WriteByte(byte('a' + g.rng.IntN(26)))
		}
		return b.String()
	case "buffer":
		opts, _ := arg.(map[string]any)
		n := g.length(opts, ctx)
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(g.rng.IntN(256))
		}
		return b
	case "container":
		fields, _ := arg.([]any)
		o := newObj(ctx)
		g.depth++
		defer func() { g.depth-- }()
		for _, f := range fields {
			fm := f.(map[string]any)
			g.field, _ = fm["name"].(string)
			v := g.gen(fm["type"], o)
			// The Reader requires a legacy_x field to repeat x (as EntityMetadata's legacy
			// data type): ProtoDef cannot say so, so the generator copies it.
			if src, ok := strings.CutPrefix(g.field, "legacy_"); ok {
				if n, ok := g.numberOf(fields, src, o); ok {
					v = n
					g.fix(o, src)
				}
			}
			if anon, _ := fm["anon"].(bool); anon {
				if sub, ok := v.(*obj); ok {
					for k, x := range sub.vals {
						o.vals[k] = x
					}
					for k := range g.fixed[sub] {
						g.fix(o, k)
					}
				}
				continue
			}
			o.vals[fm["name"].(string)] = v
		}
		return o
	case "array":
		opts := arg.(map[string]any)
		n := g.length(opts, ctx)
		out := make([]any, n)
		g.depth++
		for i := range out {
			out[i] = g.gen(opts["type"], ctx)
		}
		g.depth--
		return out
	case "option":
		if g.full() || g.rng.IntN(2) == 0 {
			return nil
		}
		return []any{g.gen(arg, ctx)}
	case "switch":
		opts := arg.(map[string]any)
		holder, name, err := resolve(opts["compareTo"].(string), ctx)
		if err != nil {
			g.conflict = true
			return nil
		}
		cur := holder.vals[name]
		_, hasDefault := opts["default"]
		fields, _ := opts["fields"].(map[string]any)
		if !g.isFixed(holder, name) {
			keys := keysOf(fields)
			// Each case, the default included, is as likely.
			if len(keys) > 0 && (!hasDefault || g.rng.IntN(len(keys)+1) != 0) {
				k := keys[g.rng.IntN(len(keys))]
				cur = g.keyValue(k, cur)
				holder.vals[name] = cur
			}
			g.fix(holder, name)
		}
		ct, ok := switchCase(opts, cur, g.vars)
		if !ok {
			g.conflict = true
			return nil
		}
		return g.gen(ct, ctx)
	case "mapper":
		opts := arg.(map[string]any)
		mappings := opts["mappings"].(map[string]any)
		if v, ok := g.accepted(); ok {
			if name, mapped := mappings[v]; mapped {
				return name
			}
			return v
		}
		keys := keysOf(mappings)
		return mappings[keys[g.rng.IntN(len(keys))]]
	case "encapsulated":
		opts := arg.(map[string]any)
		return g.gen(opts["type"], ctx)
	}
	if def, ok := g.lookup(kind); ok && arg == nil {
		if k, _ := typeKind(def); k == "container" {
			g.stack = append(g.stack, kind)
			defer func() { g.stack = g.stack[:len(g.stack)-1] }()
		}
		return g.gen(def, ctx)
	}
	g.conflict = true
	return nil
}

// numberOf returns the number held by the field name of container o, whose fields are
// defined by fields, looking a mapped name up in the field's mapper.
func (g *generator) numberOf(fields []any, name string, o *obj) (int64, bool) {
	v, ok := o.vals[name]
	if !ok {
		return 0, false
	}
	if n, ok := v.(int64); ok {
		return n, true
	}
	s, ok := v.(string)
	if !ok {
		return 0, false
	}
	for _, f := range fields {
		fm, _ := f.(map[string]any)
		if fm["name"] != name {
			continue
		}
		kind, arg := typeKind(fm["type"])
		if kind != "mapper" {
			return 0, false
		}
		opts, _ := arg.(map[string]any)
		mappings, _ := opts["mappings"].(map[string]any)
		for k, mapped := range mappings {
			if mapped == s {
				n, err := strconv.ParseInt(k, 10, 64)
				return n, err == nil
			}
		}
	}
	return 0, false
}

// keyValue turns a switch key into a value of the same kind as cur.
func (g *generator) keyValue(k string, cur any) any {
	if strings.HasPrefix(k, "/") {
		return g.vars[k[1:]]
	}
	switch cur.(type) {
	case bool:
		return k == "true"
	case string:
		return k
	}
	n, err := strconv.ParseInt(k, 10, 64)
	if err != nil {
		return k
	}
	return n
}

// ---- encoding ----

type encoder struct {
	*interp
	buf bytes.Buffer
}

func (e *encoder) uvarint(v uint64) {
	for v >= 0x80 {
		e.buf.WriteByte(byte(v) | 0x80)
		v >>= 7
	}
	e.buf.WriteByte(byte(v))
}

func asInt(v any) int64 {
	switch v := v.(type) {
	case int64:
		return v
	case bool:
		if v {
			return 1
		}
	case float64:
		return int64(v)
	}
	return 0
}

func (e *encoder) countPrefix(opts map[string]any, n int, ctx *obj) error {
	if ct, ok := opts["countType"]; ok {
		return e.write(ct, int64(n), ctx)
	}
	return nil
}

func (e *encoder) write(t any, v any, ctx *obj) error {
	kind, arg := typeKind(t)
	var b [8]byte
	switch kind {
	case "void":
		return nil
	case "bool":
		if v == true {
			e.buf.WriteByte(1)
		} else {
			e.buf.WriteByte(0)
		}
	case "u8", "i8", "byterot":
		e.buf.WriteByte(byte(asInt(v)))
	case "li16", "lu16":
		binary.LittleEndian.PutUint16(b[:], uint16(asInt(v)))
		e.buf.Write(b[:2])
	case "li32", "lu32":
		binary.LittleEndian.PutUint32(b[:], uint32(asInt(v)))
		e.buf.Write(b[:4])
	case "i32":
		binary.BigEndian.PutUint32(b[:], uint32(asInt(v)))
		e.buf.Write(b[:4])
	case "lf32":
		f, _ := v.(float64)
		binary.LittleEndian.PutUint32(b[:], math.Float32bits(float32(f)))
		e.buf.Write(b[:4])
	case "li64", "lu64":
		binary.LittleEndian.PutUint64(b[:], uint64(asInt(v)))
		e.buf.Write(b[:8])
	case "lf64":
		f, _ := v.(float64)
		binary.LittleEndian.PutUint64(b[:], math.Float64bits(f))
		e.buf.Write(b[:8])
	case "varint":
		e.uvarint(uint64(uint32(asInt(v))))
	case "varint64", "varint128":
		e.uvarint(uint64(asInt(v)))
	case "zigzag32":
		n := int32(asInt(v))
		e.uvarint(uint64(uint32((n << 1) ^ (n >> 31))))
	case "zigzag64":
		n := asInt(v)
		e.uvarint(uint64((n << 1) ^ (n >> 63)))
	case "uuid", "restBuffer":
		bs, _ := v.([]byte)
		e.buf.Write(bs)
	case "nbt", "lnbt":
		var enc nbt.Encoding = nbt.NetworkLittleEndian
		if kind == "lnbt" {
			enc = nbt.LittleEndian
		}
		if err := nbt.NewEncoderWithEncoding(&e.buf, enc).Encode(v); err != nil {
			return err
		}
	case "pstring", "buffer":
		opts, _ := arg.(map[string]any)
		var bs []byte
		switch s := v.(type) {
		case string:
			bs = []byte(s)
		case []byte:
			bs = s
		}
		if err := e.countPrefix(opts, len(bs), ctx); err != nil {
			return err
		}
		e.buf.Write(bs)
	case "container":
		fields, _ := arg.([]any)
		o, ok := v.(*obj)
		if !ok {
			return fmt.Errorf("container value %T", v)
		}
		for _, f := range fields {
			fm := f.(map[string]any)
			if anon, _ := fm["anon"].(bool); anon {
				// The case's fields were merged into o: read them from there.
				if err := e.write(fm["type"], o, o); err != nil {
					return err
				}
				continue
			}
			if err := e.write(fm["type"], o.vals[fm["name"].(string)], o); err != nil {
				return fmt.Errorf("%s: %w", fm["name"], err)
			}
		}
	case "array":
		opts := arg.(map[string]any)
		items, _ := v.([]any)
		if err := e.countPrefix(opts, len(items), ctx); err != nil {
			return err
		}
		for _, it := range items {
			if err := e.write(opts["type"], it, ctx); err != nil {
				return err
			}
		}
	case "option":
		box, _ := v.([]any)
		if len(box) == 0 {
			e.buf.WriteByte(0)
			return nil
		}
		e.buf.WriteByte(1)
		return e.write(arg, box[0], ctx)
	case "switch":
		opts := arg.(map[string]any)
		holder, name, err := resolve(opts["compareTo"].(string), ctx)
		if err != nil {
			return err
		}
		ct, ok := switchCase(opts, holder.vals[name], e.vars)
		if !ok {
			return fmt.Errorf("switch on %s: no case for %v", opts["compareTo"], holder.vals[name])
		}
		// An anonymous switch passes its parent container: give a case container a
		// scope of its own whose parent is that container.
		if o, isObj := v.(*obj); isObj && o == ctx {
			if k, _ := typeKind(ct); k == "container" || isNamedContainer(e.interp, ct) {
				return e.write(ct, &obj{vals: o.vals, parent: o}, ctx)
			}
		}
		return e.write(ct, v, ctx)
	case "mapper":
		opts := arg.(map[string]any)
		if name, ok := v.(string); ok {
			// A mapped name, or the number of a value without one.
			for k, n := range opts["mappings"].(map[string]any) {
				if n == name {
					name = k
					break
				}
			}
			num, err := strconv.ParseInt(name, 10, 64)
			if err != nil {
				return fmt.Errorf("mapper has no value %q", name)
			}
			v = num
		}
		return e.write(opts["type"], v, ctx)
	case "encapsulated":
		opts := arg.(map[string]any)
		sub := &encoder{interp: e.interp}
		if err := sub.write(opts["type"], v, ctx); err != nil {
			return err
		}
		if err := e.write(opts["lengthType"], int64(sub.buf.Len()), ctx); err != nil {
			return err
		}
		e.buf.Write(sub.buf.Bytes())
	default:
		if def, ok := e.lookup(kind); ok && arg == nil {
			return e.write(def, v, ctx)
		}
		return fmt.Errorf("unknown type %v", t)
	}
	return nil
}

func isNamedContainer(in *interp, t any) bool {
	name, ok := t.(string)
	if !ok {
		return false
	}
	def, ok := in.lookup(name)
	if !ok {
		return false
	}
	k, _ := typeKind(def)
	return k == "container"
}

// diffValues returns the paths at which two decoded values differ. A path is a chain of
// named types and the path in each, such as packet_add_player.metadata>EntityMetadata.key. Array indexes are written as [], so that a
// path names a field of a type, not of one value.
func diffValues(a, b any, path string, out map[string]bool) {
	switch x := a.(type) {
	case *obj:
		y, ok := b.(*obj)
		if !ok {
			out[path] = true
			return
		}
		// A named type starts a new part of the path, after ">": a field that the Writer
		// rewrites is rewritten wherever its type is used.
		if x.typ != "" {
			if path == "" {
				path = x.typ
			} else {
				path += ">" + x.typ
			}
		}
		for _, k := range keysOf(x.vals) {
			diffValues(x.vals[k], y.vals[k], path+"."+k, out)
		}
		for _, k := range keysOf(y.vals) {
			if _, ok := x.vals[k]; !ok {
				out[path+"."+k] = true
			}
		}
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			out[path] = true
			return
		}
		for i := range x {
			diffValues(x[i], y[i], path+"[]", out)
		}
	case []byte:
		y, ok := b.([]byte)
		if !ok || !bytes.Equal(x, y) {
			out[path] = true
		}
	case map[string]any, []map[string]any:
		if fmt.Sprint(a) != fmt.Sprint(b) {
			out[path] = true
		}
	default:
		if a != b {
			out[path] = true
		}
	}
}
