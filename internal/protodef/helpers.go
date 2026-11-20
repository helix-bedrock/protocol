package protodef

import (
	"go/ast"
	"go/types"
)

// helper runs a call of one of the generic helpers in io.go, which are read by what
// they do rather than by walking their bodies. It reports false for other functions.
func (g *gen) helper(name string, c *ast.CallExpr, e *env, sc *scope) bool {
	arg := func(i int) ast.Expr {
		if i < len(c.Args) {
			return c.Args[i]
		}
		return nil
	}
	switch name {
	case "Slice", "SliceMax":
		g.sliceHelper(c, arg(1), "varint", e, sc)
	case "SliceUint8Length":
		g.sliceHelper(c, arg(1), "u8", e, sc)
	case "SliceUint32Length":
		g.sliceHelper(c, arg(1), "lu32", e, sc)
	case "SliceOfLen", "sliceOfLenMax":
		g.countedHelper(c, arg(1), arg(2), nil, e, sc)
	case "FuncSlice", "FuncIOSlice":
		g.funcSliceHelper(c, arg(1), arg(2), "varint", e, sc)
	case "FuncSliceUint32Length":
		g.funcSliceHelper(c, arg(1), arg(2), "lu32", e, sc)
	case "FuncSliceOfLen", "FuncIOSliceOfLen":
		g.countedHelper(c, arg(1), arg(2), arg(3), e, sc)
	case "Single":
		place, ok := g.target(arg(1), e)
		if !ok {
			g.diag(sc, c.Pos(), "cannot tell what Single reads into")
			return true
		}
		g.add(sc, e, place, g.marshalerType(g.p.info.TypeOf(arg(1)), sc, c.Pos()))
	case "OptionalFunc":
		place, ok := g.target(arg(1), e)
		if !ok {
			g.diag(sc, c.Pos(), "cannot tell what OptionalFunc reads into")
			return true
		}
		g.add(sc, e, place, option(g.funcValueType(arg(2), place+".val", e, sc)))
		g.optionValue(place, sc)
	case "OptionalMarshaler":
		place, ok := g.target(arg(1), e)
		if !ok {
			g.diag(sc, c.Pos(), "cannot tell what OptionalMarshaler reads into")
			return true
		}
		g.add(sc, e, place, option(g.marshalerType(optionalElem(g.p.info.TypeOf(arg(1))), sc, c.Pos())))
		g.optionValue(place, sc)
	case "IntegerFunc":
		place, ok := g.target(arg(0), e)
		if !ok {
			g.diag(sc, c.Pos(), "cannot tell what IntegerFunc reads into")
			return true
		}
		g.addField(sc, e, place, g.funcValueType(arg(1), place, e, sc), true)
	default:
		return false
	}
	return true
}

func (g *gen) sliceHelper(c *ast.CallExpr, x ast.Expr, countType string, e *env, sc *scope) {
	place, ok := g.target(x, e)
	if !ok {
		g.diag(sc, c.Pos(), "cannot tell what the slice is read into")
		return
	}
	g.add(sc, e, place, arrayOf(countType, g.marshalerType(sliceElem(g.p.info.TypeOf(x)), sc, c.Pos())))
}

func (g *gen) funcSliceHelper(c *ast.CallExpr, x, f ast.Expr, countType string, e *env, sc *scope) {
	place, ok := g.target(x, e)
	if !ok {
		g.diag(sc, c.Pos(), "cannot tell what the slice is read into")
		return
	}
	g.add(sc, e, place, arrayOf(countType, g.funcValueType(f, place+"[]", e, sc)))
}

// countedHelper reads a slice whose length was read before; f is nil for a slice of
// Marshalers.
func (g *gen) countedHelper(c *ast.CallExpr, l, x, f ast.Expr, e *env, sc *scope) {
	place, ok := g.target(x, e)
	if !ok {
		g.diag(sc, c.Pos(), "cannot tell what the slice is read into")
		return
	}
	count, ok := g.countRef(l, e, sc)
	if !ok {
		g.diag(sc, c.Pos(), "slice length %s is not a field read before", types.ExprString(l))
		return
	}
	var elem any
	if f == nil {
		elem = g.marshalerType(sliceElem(g.p.info.TypeOf(x)), sc, c.Pos())
	} else {
		elem = g.funcValueType(f, place+"[]", e, sc)
	}
	g.add(sc, e, place, arrayCount(count, elem))
}

// sliceElem returns T for *[]T or []T.
func sliceElem(t types.Type) types.Type {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if s, ok := t.Underlying().(*types.Slice); ok {
		return s.Elem()
	}
	return t
}

// optionalElem returns T for *Optional[T].
func optionalElem(t types.Type) types.Type {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := t.(*types.Named); ok && n.TypeArgs().Len() == 1 {
		return n.TypeArgs().At(0)
	}
	return t
}

// optionValue makes the value inside the option at place stand for the option's field:
// a ProtoDef switch on an option compares its value (or no value). Code reaches the
// value through Optional.Value, as in if v, ok := x.Value(); ok { ... }.
func (g *gen) optionValue(place string, sc *scope) {
	if name := sc.places[place]; name != "" {
		sc.places[place+".val"] = name
	}
}
