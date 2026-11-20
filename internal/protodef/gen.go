package protodef

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"
)

// gen walks Marshal bodies as the Reader runs them and builds ProtoDef types.
type gen struct {
	p      *program
	ioType types.Type
	reader types.Type

	// named holds the shared types. Generate writes them sorted by name.
	named *Map
	// names maps the object a named type comes from to its name.
	names map[any]string
	// owners maps each type name to the object it comes from.
	owners map[string]any
	// lookups caches the tables of lookup functions such as lookupEvent.
	lookups map[*types.Func]*lookupTable
	// inlining is the stack of functions being inlined, to catch recursion.
	inlining map[*types.Func]bool

	diags []string
	// stack holds the names of the types being built.
	stack []string
	// values maps type name, then field name, to the only values the Reader accepts.
	values map[string]map[string][]string
	// consts holds the constants each field is compared with (see mapper.go).
	consts map[*Field]map[*types.Const]bool
	// uses holds every switch on a field, whose keys applyMappers renames.
	uses []switchUse
	// overrides holds the hand-written IO method types of overrides.go.
	overrides map[string]func(g *gen) any
	// goFields maps a type name, then the Go field path a field of the type is read
	// into, to that field. The test uses it to compare values with the Go structs.
	goFields map[string]map[string]*Field
	// visited holds every call the walk ran, and entered every body it walked. A call
	// in an entered body that reads from the IO but was never run is code the walk
	// skipped (see skippedCalls).
	visited map[*ast.CallExpr]bool
	entered map[*ast.BlockStmt]bool
}

// diag reports code at pos that the generator cannot turn into ProtoDef.
func (g *gen) diag(sc *scope, pos token.Pos, format string, args ...any) {
	sc.diags = append(sc.diags, fmt.Sprintf("%s: %s", g.p.fset.Position(pos), fmt.Sprintf(format, args...)))
}

// reserve returns the name of the shared type for key. It reports false when the type
// exists already; otherwise it takes a free name based on base (a Go type of another
// package can have the same name).
func (g *gen) reserve(key any, base string) (string, bool) {
	if n, ok := g.names[key]; ok {
		return n, false
	}
	name := base
	for i := 2; ; i++ {
		if _, taken := g.owners[name]; !taken {
			break
		}
		name = base + strconv.Itoa(i)
	}
	g.names[key] = name
	g.owners[name] = key
	return name, true
}

// namedType returns the name of the shared type made by build for key, building it the
// first time. The name is set before building, so recursive types refer to it.
func (g *gen) namedType(key any, name string, build func(sc *scope)) string {
	name, ok := g.reserve(key, name)
	if !ok {
		return name
	}
	g.named.Set(name, nil)
	sc := newScope(nil, true)
	g.stack = append(g.stack, name)
	build(sc)
	g.stack = g.stack[:len(g.stack)-1]
	g.named.Set(name, container(sc.fields))
	g.diags = append(g.diags, sc.diags...)
	return name
}

// fixedType registers a type that is written by hand.
func (g *gen) fixedType(key any, name string, build func() any) string {
	name, ok := g.reserve(key, name)
	if ok {
		g.named.Set(name, build())
	}
	return name
}

// namedBody returns the named type read by running the body of fn with binds.
func (g *gen) namedBody(fn *types.Func, name string, binds map[int]*binding) string {
	return g.namedType(fn, name, func(sc *scope) { g.body(fn, binds, sc, "") })
}

// receiverBinds binds a Marshal(IO) method: the receiver is the value read.
func receiverBinds() map[int]*binding { return map[int]*binding{-1: {place: "$"}, 0: {io: true}} }

// argumentBinds binds a func(IO, *T): the second argument is the value read.
func argumentBinds() map[int]*binding { return map[int]*binding{0: {io: true}, 1: {place: "$"}} }

// methodType returns the named type read by a Marshal method.
func (g *gen) methodType(fn *types.Func) string {
	return g.namedBody(fn, typeName(fn), receiverBinds())
}

// typeName names the type of a method's receiver, or a package-level function.
func typeName(fn *types.Func) string {
	name := fn.Name()
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		t := sig.Recv().Type()
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		if n, ok := t.(*types.Named); ok {
			name = n.Obj().Name()
		}
	}
	return exported(name)
}

func exported(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// topMarshalerType is marshalerType outside any type being built.
func (g *gen) topMarshalerType(t types.Type) any {
	sc := newScope(nil, true)
	typ := g.marshalerType(t, sc, token.NoPos)
	g.diags = append(g.diags, sc.diags...)
	return typ
}

// marshalerType returns the named type of t, read by the Marshal method of *t.
func (g *gen) marshalerType(t types.Type, sc *scope, pos token.Pos) any {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	ms := types.NewMethodSet(types.NewPointer(t))
	sel := ms.Lookup(g.p.root, "Marshal")
	if sel == nil {
		if n, ok := t.(*types.Named); ok {
			sel = ms.Lookup(n.Obj().Pkg(), "Marshal")
		}
	}
	if sel == nil {
		g.diag(sc, pos, "type %s has no Marshal method", t)
		return "void"
	}
	fn, ok := sel.Obj().(*types.Func)
	if !ok {
		g.diag(sc, pos, "Marshal of %s is not a method", t)
		return "void"
	}
	return g.methodType(fn)
}

// funcType returns the named type read by a helper func(IO, *T, ...).
func (g *gen) funcType(fn *types.Func) string {
	name := fn.Name()
	// marshalDataStorePropertyValue reads a DataStorePropertyValue.
	if rest, ok := strings.CutPrefix(name, "marshal"); ok && rest != "" {
		name = rest
	}
	return g.namedBody(fn, exported(name), argumentBinds())
}

// collect builds the type read by run: void for nothing, the field's own type for one
// field, and a container for more.
func (g *gen) collect(sc *scope, run func(*scope)) any {
	probe := newScope(sc, true)
	run(probe)
	if len(probe.fields) == 1 && !probe.fields[0].Anon {
		flat := newScope(sc, false)
		run(flat)
		if len(flat.fields) == 1 && !flat.fields[0].Anon {
			sc.diags = append(sc.diags, flat.diags...)
			return flat.fields[0].Type
		}
	}
	sc.diags = append(sc.diags, probe.diags...)
	if len(probe.fields) == 0 {
		return "void"
	}
	return container(probe.fields)
}

// isIOType reports whether t is protocol.IO or *protocol.Reader.
func (g *gen) isIOType(t types.Type) bool {
	return t != nil && (types.Identical(t, g.ioType) || types.Identical(t, g.reader))
}

// skippedCalls reports the calls that read from the IO in a body the walk entered, but
// that it never ran: code in a statement or expression the walk does not follow, such
// as a defer or a call inside a composite literal. Without this the schema would just
// lack the fields they read.
func (g *gen) skippedCalls() {
	for body := range g.entered {
		// Function literals are included: one the walk never ran (passed to a function it
		// does not follow) has reads that are missing too.
		ast.Inspect(body, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok || g.visited[c] {
				return true
			}
			reads := false
			if sel, ok := unparen(c.Fun).(*ast.SelectorExpr); ok && g.isIOType(g.p.info.TypeOf(sel.X)) && !isIOIgnored(sel.Sel.Name) {
				reads = true
			}
			for _, a := range c.Args {
				if g.isIOType(g.p.info.TypeOf(a)) {
					reads = true
				}
			}
			if reads {
				g.diags = append(g.diags, fmt.Sprintf("%s: call %s is never run by the walk", g.p.fset.Position(c.Pos()), types.ExprString(c)))
			}
			return true
		})
	}
}
