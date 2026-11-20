package protodef

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

// isIO reports whether x is the IO being read from: a protocol.IO or a *protocol.Reader.
func (g *gen) isIO(x ast.Expr, e *env) bool {
	if id, ok := unparen(x).(*ast.Ident); ok {
		if b := e.lookup(g.obj(id)); b != nil {
			return b.io
		}
	}
	t := g.p.info.TypeOf(x)
	return t != nil && (types.Identical(t, g.ioType) || types.Identical(t, g.reader))
}

func (g *gen) obj(id *ast.Ident) types.Object {
	if o := g.p.info.Uses[id]; o != nil {
		return o
	}
	return g.p.info.Defs[id]
}

// place returns a key naming the Go value x refers to.
func (g *gen) place(x ast.Expr, e *env) (string, bool) {
	switch x := unparen(x).(type) {
	case *ast.Ident:
		o := g.obj(x)
		if o == nil {
			return "", false
		}
		if b := e.lookup(o); b != nil && b.place != "" {
			return b.place, true
		}
		if v, ok := o.(*types.Var); ok {
			if v.Parent() == v.Pkg().Scope() {
				return "G:" + v.Name(), true
			}
			return fmt.Sprintf("L:%s@%d", v.Name(), v.Pos()), true
		}
		return "", false
	case *ast.SelectorExpr:
		if sel, ok := g.p.info.Selections[x]; ok && sel.Kind() == types.FieldVal {
			base, ok := g.place(x.X, e)
			if !ok {
				return "", false
			}
			return base + "." + x.Sel.Name, true
		}
		if o, ok := g.p.info.Uses[x.Sel].(*types.Var); ok {
			return "G:" + o.Name(), true
		}
		return "", false
	case *ast.StarExpr:
		return g.place(x.X, e)
	case *ast.UnaryExpr:
		if x.Op == token.AND {
			return g.place(x.X, e)
		}
	case *ast.IndexExpr:
		base, ok := g.place(x.X, e)
		if !ok {
			return "", false
		}
		if v := g.p.info.Types[x.Index].Value; v != nil {
			return base + "[" + v.ExactString() + "]", true
		}
		return base + "[]", true
	case *ast.SliceExpr:
		return g.place(x.X, e)
	case *ast.CallExpr:
		if tv, ok := g.p.info.Types[x.Fun]; ok && tv.IsType() && len(x.Args) == 1 {
			return g.place(x.Args[0], e)
		}
	}
	return "", false
}

// target returns the place a value is read into. A local holding a copy of another
// value stops standing for it once something is read into the copy.
func (g *gen) target(x ast.Expr, e *env) (string, bool) {
	inner := unparen(x)
	if u, ok := inner.(*ast.UnaryExpr); ok && u.Op == token.AND {
		inner = unparen(u.X)
	}
	if id, ok := inner.(*ast.Ident); ok {
		if o := g.obj(id); o != nil {
			if b := e.lookup(o); b != nil && b.copy {
				fresh := fmt.Sprintf("L:%s@%d", o.Name(), o.Pos())
				e.vars[o] = &binding{place: fresh}
				return fresh, true
			}
		}
	}
	return g.place(x, e)
}

// placeName makes a field name from the last part of a place.
func placeName(place string) string {
	if place == "" {
		return ""
	}
	if strings.HasPrefix(place, "L:") || strings.HasPrefix(place, "G:") {
		place = place[2:]
		if i := strings.IndexByte(place, '@'); i >= 0 {
			rest := place[i:]
			place = place[:i]
			// A local's field: keep what follows the position.
			if j := strings.IndexByte(rest, '.'); j >= 0 {
				place = rest[j+1:]
			}
		}
	}
	if i := strings.LastIndexByte(place, '.'); i >= 0 {
		place = place[i+1:]
	}
	idx := ""
	if i := strings.IndexByte(place, '['); i >= 0 {
		for _, part := range strings.Split(place[i:], "[") {
			part = strings.TrimSuffix(part, "]")
			if part != "" {
				idx += "_" + part
			}
		}
		place = place[:i]
	}
	if place == "$" {
		place = ""
	}
	name := strings.TrimPrefix(snake(place)+idx, "_")
	// An element of the value itself, such as x[0] of a [3]int8: ProtoDef compilers use
	// field names as identifiers, which cannot start with a digit.
	if name != "" && name[0] >= '0' && name[0] <= '9' {
		name = "value_" + name
	}
	return name
}

// snake turns a Go identifier into snake_case: MoveActorAbsolute -> move_actor_absolute,
// NPCRequest -> npc_request, EntityRuntimeIDs -> entity_runtime_ids.
func snake(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		upper := r >= 'A' && r <= 'Z'
		if upper && i > 0 {
			prev := rs[i-1]
			prevLower := (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9')
			nextLower := i+1 < len(rs) && rs[i+1] >= 'a' && rs[i+1] <= 'z'
			// "IDs" is the plural of ID, not I + Ds.
			pluralS := i+1 < len(rs) && rs[i+1] == 's' && (i+2 == len(rs) || (rs[i+2] >= 'A' && rs[i+2] <= 'Z'))
			if prevLower || (nextLower && !pluralS && prev >= 'A' && prev <= 'Z') {
				b.WriteByte('_')
			}
		}
		if upper {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func unparen(x ast.Expr) ast.Expr {
	for {
		p, ok := x.(*ast.ParenExpr)
		if !ok {
			return x
		}
		x = p.X
	}
}

// bind records what a local assigned from rhs stands for.
func (g *gen) bind(lhs, rhs ast.Expr, e *env, define bool) {
	id, ok := unparen(lhs).(*ast.Ident)
	if !ok || id.Name == "_" {
		return
	}
	o := g.obj(id)
	if o == nil {
		return
	}
	if fl, ok := unparen(rhs).(*ast.FuncLit); ok {
		e.vars[o] = &binding{fn: fl, fnEnv: e}
		return
	}
	if g.isIO(rhs, e) {
		e.vars[o] = &binding{io: true}
		return
	}
	// A local taking the address of a value, or copying a reference to it (a pointer or
	// interface), stands for that value. A copy of a plain value stands for it only
	// until something is read into the copy (see target).
	if p, ok := g.place(rhs, e); ok && !strings.HasPrefix(p, "G:") {
		u, isAddr := unparen(rhs).(*ast.UnaryExpr)
		if isAddr && u.Op == token.AND || isPlaceExpr(rhs) && isReference(g.p.info.TypeOf(rhs)) {
			e.vars[o] = &binding{place: p}
			return
		}
		if isPlaceExpr(rhs) {
			e.vars[o] = &binding{place: p, copy: true}
			return
		}
	}
	if !define {
		// An assignment of a computed value: the local no longer stands for another value.
		if b := e.lookup(o); b != nil && b.place != "" {
			e.vars[o] = &binding{place: fmt.Sprintf("L:%s@%d", o.Name(), o.Pos())}
		}
	}
}

// alias makes a field read before also stand for lhs, for code that reads into a local
// and then stores it: x.IdentityType = byte(variant).
func (g *gen) alias(lhs, rhs ast.Expr, e *env, sc *scope) {
	lp, ok1 := g.place(lhs, e)
	rp, ok2 := g.place(rhs, e)
	if !ok1 || !ok2 {
		return
	}
	if s, name, _ := sc.owner(rp); name != "" {
		s.places[lp] = name
	}
}

// isReference reports whether a copy of a value of type t still refers to the same value.
func isReference(t types.Type) bool {
	if t == nil {
		return false
	}
	switch t.Underlying().(type) {
	case *types.Pointer, *types.Interface, *types.Slice, *types.Map:
		return true
	}
	return false
}

// isPlaceExpr reports whether x is a field selector or a dereference.
func isPlaceExpr(x ast.Expr) bool {
	switch unparen(x).(type) {
	case *ast.SelectorExpr:
		return true
	case *ast.StarExpr:
		return true
	}
	return false
}

// optionalValue binds v in v, ok := x.Value() for a protocol.Optional x to the value
// inside x. It reports false for other statements.
func (g *gen) optionalValue(lhs, rhs ast.Expr, e *env) bool {
	c, ok := unparen(rhs).(*ast.CallExpr)
	if !ok || len(c.Args) != 0 {
		return false
	}
	sel, ok := unparen(c.Fun).(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Value" {
		return false
	}
	n, ok := pointee(g.p.info.TypeOf(sel.X)).(*types.Named)
	if !ok || n.Obj().Pkg() != g.p.root || n.Obj().Name() != "Optional" {
		return false
	}
	p, ok := g.place(sel.X, e)
	if !ok {
		return false
	}
	if id, ok := unparen(lhs).(*ast.Ident); ok && id.Name != "_" {
		if o := g.obj(id); o != nil {
			e.vars[o] = &binding{place: p + ".val", copy: true}
		}
	}
	return true
}

// pointee returns the type a pointer type points to, or t.
func pointee(t types.Type) types.Type {
	if p, ok := t.(*types.Pointer); ok {
		return p.Elem()
	}
	return t
}
