package protodef

import (
	"go/ast"
	"go/constant"
	"go/types"
	"strconv"
	"strings"
)

// binding is what a local variable or parameter stands for.
type binding struct {
	place string
	// copy is set for a local holding a copy of the value at place.
	copy  bool
	io    bool
	fn    *ast.FuncLit
	fnEnv *env
}

// env holds the bindings of one function body.
type env struct {
	vars   map[types.Object]*binding
	parent *env
	// ioName names the first local field read while inlining an IO method ioInline lists.
	ioName string
}

func newEnv(parent *env) *env { return &env{vars: map[types.Object]*binding{}, parent: parent} }

func (e *env) lookup(o types.Object) *binding {
	for ; e != nil; e = e.parent {
		if b, ok := e.vars[o]; ok {
			return b
		}
	}
	return nil
}

// scope is a ProtoDef container being built.
type scope struct {
	parent *scope
	// level is true for scopes that are their own container in the schema, so that a
	// reference from inside to a field outside needs "../".
	level  bool
	fields []*Field
	used   map[string]int
	// places maps the Go value read into a field to the name of the field.
	places map[string]string
	// dyn holds what decides the concrete type of interface values read so far.
	dyn map[string]*dynamic
	// facts holds values that are not read but set from constants, possibly depending
	// on a field read before (see derive.go). A place set here is not a wire field any
	// more: places holds "" for it.
	facts map[string]*fact
	diags []string
	// errored is set when the scope reports invalid data (UnknownEnumOption and the like).
	errored bool
}

func newScope(parent *scope, level bool) *scope {
	return &scope{
		parent: parent, level: level, used: map[string]int{}, places: map[string]string{},
		dyn: map[string]*dynamic{}, facts: map[string]*fact{},
	}
}

// dynamic says that an interface value gets its concrete type from a field read before.
type dynamic struct {
	idPlace string
	table   *lookupTable
}

// add appends a field read into place to sc.
func (g *gen) add(sc *scope, e *env, place string, typ any) {
	g.addField(sc, e, place, typ, false)
}

// addField adds a field like add. converted is set for a value stored in a Go type other
// than the one read (IntegerFunc), which the test compares by bits only.
func (g *gen) addField(sc *scope, e *env, place string, typ any, converted bool) {
	name := placeName(place)
	if e != nil && e.ioName != "" && strings.HasPrefix(place, "L:") {
		name = e.ioName
		e.ioName = ""
	}
	f := &Field{Name: sc.uniq(name), Type: typ}
	sc.fields = append(sc.fields, f)
	if place != "" {
		sc.places[place] = f.Name
		sc.facts[place] = nil
	}
	if path, ok := strings.CutPrefix(place, "$."); ok && len(g.stack) > 0 && isSelectorPath(path) {
		typ := g.stack[len(g.stack)-1]
		if g.goFields[typ] == nil {
			g.goFields[typ] = map[string]*Field{}
		}
		// A value read twice ends up holding what was read last.
		delete(g.goFields[typ], path)
		delete(g.goFields[typ], "~"+path)
		if converted {
			path = "~" + path
		}
		g.goFields[typ][path] = f
	}
}

// isSelectorPath reports whether path is a chain of field names, such as Stack.Count.
func isSelectorPath(path string) bool {
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
				return false
			}
		}
	}
	return true
}

func (sc *scope) uniq(name string) string {
	if name == "" {
		name = "value"
	}
	n := sc.used[name]
	sc.used[name] = n + 1
	if n == 0 {
		return name
	}
	return name + "_" + strconv.Itoa(n+1)
}

// owner returns the innermost scope from sc up that knows place, the name of its field
// there ("" when the place is not a wire field any more), and how many containers lie
// between sc and that scope.
func (sc *scope) owner(place string) (*scope, string, int) {
	up := 0
	for s := sc; s != nil; s = s.parent {
		if name, ok := s.places[place]; ok {
			return s, name, up
		}
		if s.level {
			up++
		}
	}
	return nil, "", 0
}

// ref returns the ProtoDef path of the field holding place, as seen from sc.
func (g *gen) ref(place string, sc *scope) (string, bool) {
	_, name, up := sc.owner(place)
	if name == "" {
		return "", false
	}
	return strings.Repeat("../", up) + name, true
}

// dyn returns what decides the concrete type of the interface value at place.
func (g *gen) dyn(place string, sc *scope) *dynamic {
	for s := sc; s != nil; s = s.parent {
		if d, ok := s.dyn[place]; ok {
			return d
		}
	}
	return nil
}

// commit moves the places, dynamic types and diagnostics of a finished child scope into
// its parent. The fields of the child stay where they are: they end up in a container
// or type of their own, but a later field can still refer to them by name, as ProtoDef
// merges anonymous switches into the parent.
func (sc *scope) commit(child *scope, rename func(string) string) {
	for p, n := range child.places {
		if rename != nil {
			n = rename(n)
		}
		if n != "" {
			sc.places[p] = n
		}
	}
	for p, d := range child.dyn {
		sc.dyn[p] = d
	}
	sc.diags = append(sc.diags, child.diags...)
}

// countRef returns the ProtoDef count of an array: a number or a path to a field.
func (g *gen) countRef(x ast.Expr, e *env, sc *scope) (any, bool) {
	if v := g.p.info.Types[x].Value; v != nil {
		if n, ok := constant.Int64Val(v); ok {
			return n, true
		}
	}
	p, ok := g.place(x, e)
	if !ok {
		return nil, false
	}
	r, ok := g.ref(p, sc)
	if !ok {
		return nil, false
	}
	if strings.Contains(r, "/") {
		// ProtoDefCompiler pastes a count into JavaScript as it is, so it cannot be a path.
		g.diag(sc, x.Pos(), "array count %s is a field of an outer container", r)
		return r, true
	}
	if !isUnsafeCountName(r) {
		return r, true
	}
	// The compiled array code has locals of these names, and the compiler renames fields
	// called value, size and the like: give the field a name that is pasted as is.
	if s, _, _ := sc.owner(p); s != nil {
		for _, f := range s.fields {
			if f.Name == r && !f.Anon {
				f.Name = s.uniq("length")
				for q, n := range s.places {
					if n == r {
						s.places[q] = f.Name
					}
				}
				return f.Name, true
			}
		}
	}
	g.diag(sc, x.Pos(), "array count field %q cannot be renamed", r)
	return r, true
}

// isUnsafeCountName reports whether an array count cannot refer to a field of this name:
// a local of the code ProtoDefCompiler generates for an array, or a name it renames in
// containers.
func isUnsafeCountName(name string) bool {
	switch name {
	case "count", "countSize", "data", "size", "i", "elem", "value", "enum", "default", "offset", "buffer", "ctx":
		return true
	}
	return false
}
