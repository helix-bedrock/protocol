package protodef

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"strconv"
)

// lookupTable is the switch of a lookup function: case value to concrete type.
type lookupTable struct {
	keys  []string
	types map[string]types.Type
	// returns is set for lookups that return the value instead of storing it.
	returns bool
	// consts holds the constant each case names.
	consts map[string]*types.Const
}

// lookupAssign handles x, ok := lookup(id) for lookup functions that return the
// concrete value.
func (g *gen) lookupAssign(lhs, rhs ast.Expr, e *env, sc *scope) bool {
	c, ok := unparen(rhs).(*ast.CallExpr)
	if !ok || len(c.Args) != 1 {
		return false
	}
	id, ok := unparen(c.Fun).(*ast.Ident)
	if !ok {
		return false
	}
	fn, ok := g.obj(id).(*types.Func)
	if !ok {
		return false
	}
	t := g.lookupTable(fn)
	if t == nil || !t.returns {
		return false
	}
	idPlace, ok1 := g.place(c.Args[0], e)
	target, ok2 := g.place(lhs, e)
	if ok1 && ok2 {
		sc.dyn[target] = &dynamic{idPlace: idPlace, table: t}
	}
	return true
}

// lookupTable recognises a lookup function: func(id T, x *I) bool, or func(id T) (I,
// bool), whose body is a switch on id with cases that set *x = &Concrete{} or return
// &Concrete{}. It returns nil for other functions.
func (g *gen) lookupTable(fn *types.Func) *lookupTable {
	if t, ok := g.lookups[fn]; ok {
		return t
	}
	g.lookups[fn] = nil
	decl, ok := g.p.funcs[fn.Origin()]
	if !ok {
		return nil
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return nil
	}
	var iface types.Type
	returns := false
	switch {
	case sig.Params().Len() == 2 && sig.Results().Len() == 1:
		ptr, ok := sig.Params().At(1).Type().(*types.Pointer)
		if !ok {
			return nil
		}
		iface = ptr.Elem()
	case sig.Params().Len() == 1 && sig.Results().Len() == 2:
		iface, returns = sig.Results().At(0).Type(), true
	default:
		return nil
	}
	if _, ok := iface.Underlying().(*types.Interface); !ok {
		return nil
	}
	var sw *ast.SwitchStmt
	for _, s := range decl.Body.List {
		if s, ok := s.(*ast.SwitchStmt); ok {
			sw = s
		}
	}
	if sw == nil || sw.Tag == nil {
		return nil
	}
	t := &lookupTable{types: map[string]types.Type{}, returns: returns, consts: map[string]*types.Const{}}
	var unseen []token.Pos
	for _, s := range sw.Body.List {
		cc, ok := s.(*ast.CaseClause)
		if !ok {
			continue
		}
		var concrete types.Type
		for _, s := range cc.Body {
			var x ast.Expr
			switch s := s.(type) {
			case *ast.AssignStmt:
				if len(s.Rhs) == 1 {
					x = s.Rhs[0]
				}
			case *ast.ReturnStmt:
				if len(s.Results) > 0 {
					x = s.Results[0]
				}
			}
			if u, ok := unparen(x).(*ast.UnaryExpr); ok && u.Op == token.AND {
				concrete = g.p.info.TypeOf(u.X)
			}
		}
		if concrete == nil {
			if !onlyReturnsFalse(cc.Body) {
				unseen = append(unseen, cc.Pos())
			}
			continue
		}
		if cc.List == nil {
			// A default case that picks a type: the switch would need a default.
			unseen = append(unseen, cc.Pos())
			continue
		}
		for _, k := range cc.List {
			v := g.p.info.Types[k].Value
			if v == nil {
				return nil
			}
			key := constKey(v)
			t.keys = append(t.keys, key)
			t.types[key] = concrete
			t.consts[key] = g.constOf(k)
		}
	}
	if len(t.keys) == 0 {
		return nil
	}
	// Report these only now, so that other func(id, *I) bool functions stay quiet. A case
	// left out would just be missing from the switch of the polymorphic value.
	for _, pos := range unseen {
		g.diags = append(g.diags, fmt.Sprintf("%s: case of lookup %s without a concrete type the generator can see (*x = &T{} or return &T{}, ...)", g.p.fset.Position(pos), fn.Name()))
	}
	g.lookups[fn] = t
	return t
}

// onlyReturnsFalse reports whether a case body only rejects the value.
func onlyReturnsFalse(body []ast.Stmt) bool {
	if len(body) != 1 {
		return false
	}
	ret, ok := body[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) == 0 {
		return false
	}
	id, ok := ret.Results[len(ret.Results)-1].(*ast.Ident)
	return ok && id.Name == "false"
}

func constKey(v constant.Value) string {
	switch v.Kind() {
	case constant.Bool:
		return strconv.FormatBool(constant.BoolVal(v))
	case constant.String:
		return constant.StringVal(v)
	}
	return v.ExactString()
}
