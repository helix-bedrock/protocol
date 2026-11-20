package protodef

import (
	"go/ast"
	"go/token"
	"go/types"
)

// stmts runs a statement list. It reports whether the list ends in a return.
func (g *gen) stmts(list []ast.Stmt, e *env, sc *scope) bool {
	for i, s := range list {
		switch s := s.(type) {
		case *ast.ReturnStmt:
			for _, r := range s.Results {
				g.exprCalls(r, e, sc)
			}
			return true
		case *ast.IfStmt:
			consumed, term := g.ifStmt(s, list[i+1:], e, sc)
			if consumed {
				return term
			}
			if term {
				return true
			}
		default:
			if g.stmt(s, e, sc) {
				return true
			}
		}
	}
	return false
}

func (g *gen) stmt(s ast.Stmt, e *env, sc *scope) bool {
	switch s := s.(type) {
	case *ast.ExprStmt:
		g.exprCalls(s.X, e, sc)
	case *ast.AssignStmt:
		if len(s.Rhs) == 1 && g.lookupAssign(s.Lhs[0], s.Rhs[0], e, sc) {
			return false
		}
		if len(s.Lhs) == 2 && len(s.Rhs) == 1 && g.optionalValue(s.Lhs[0], s.Rhs[0], e) {
			return false
		}
		for _, r := range s.Rhs {
			g.exprCalls(r, e, sc)
		}
		if len(s.Lhs) == len(s.Rhs) {
			for i, l := range s.Lhs {
				g.bind(l, s.Rhs[i], e, s.Tok == token.DEFINE)
				if v := g.p.info.Types[s.Rhs[i]].Value; v != nil {
					g.setConst(l, v, e, sc)
				} else if _, isLocal := unparen(l).(*ast.Ident); s.Tok == token.ASSIGN && !isLocal {
					g.alias(l, s.Rhs[i], e, sc)
				}
			}
		}
	case *ast.DeclStmt:
		gd, ok := s.Decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			return false
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, v := range vs.Values {
				g.exprCalls(v, e, sc)
			}
			if len(vs.Values) == len(vs.Names) {
				for i, n := range vs.Names {
					g.bind(n, vs.Values[i], e, true)
					if v := g.p.info.Types[vs.Values[i]].Value; v != nil {
						g.setConst(n, v, e, sc)
					}
				}
			}
		}
	case *ast.BlockStmt:
		return g.stmts(s.List, e, sc)
	case *ast.SwitchStmt:
		g.switchStmt(s, e, sc)
	case *ast.ForStmt:
		g.forStmt(s, e, sc)
	case *ast.RangeStmt:
		g.rangeStmt(s, e, sc)
	case *ast.LabeledStmt:
		return g.stmt(s.Stmt, e, sc)
	case *ast.TypeSwitchStmt:
		probe := newScope(sc, true)
		g.stmts(s.Body.List, newEnv(e), probe)
		if len(probe.fields) > 0 {
			g.diag(sc, s.Pos(), "type switch that reads fields")
		}
	case *ast.BranchStmt:
		// break, continue, goto and fallthrough make which reads run depend on values,
		// which the walk does not follow.
		g.diag(sc, s.Pos(), "%s changes which reads run", s.Tok)
	case *ast.IncDecStmt, *ast.EmptyStmt, *ast.DeferStmt, *ast.GoStmt, *ast.SendStmt:
	default:
		g.diag(sc, s.Pos(), "unsupported statement %T", s)
	}
	return false
}

// exprCalls runs the calls in x that can read from the IO or decide a dynamic type.
func (g *gen) exprCalls(x ast.Expr, e *env, sc *scope) {
	switch x := unparen(x).(type) {
	case *ast.CallExpr:
		g.call(x, e, sc)
	case *ast.UnaryExpr:
		g.exprCalls(x.X, e, sc)
	case *ast.BinaryExpr:
		g.exprCalls(x.X, e, sc)
		g.exprCalls(x.Y, e, sc)
	}
}

// call runs one call.
func (g *gen) call(c *ast.CallExpr, e *env, sc *scope) {
	g.visited[c] = true
	fun := unparen(c.Fun)
	// Explicit instantiations such as SliceOfLen[T, S, A](...).
	switch f := fun.(type) {
	case *ast.IndexExpr:
		fun = f.X
	case *ast.IndexListExpr:
		fun = f.X
	}
	if tv, ok := g.p.info.Types[c.Fun]; ok && tv.IsType() {
		for _, a := range c.Args {
			g.exprCalls(a, e, sc)
		}
		return
	}
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		if g.isIO(f.X, e) {
			g.ioCall(f.Sel.Name, c, e, sc)
			return
		}
		if sel, ok := g.p.info.Selections[f]; ok {
			fn, ok := sel.Obj().(*types.Func)
			if !ok {
				return
			}
			g.methodCall(fn, f.X, c, e, sc)
			return
		}
		if fn, ok := g.p.info.Uses[f.Sel].(*types.Func); ok {
			g.funcCall(fn, c, e, sc)
		}
	case *ast.Ident:
		o := g.obj(f)
		if b := e.lookup(o); b != nil && b.fn != nil {
			g.inlineLit(b.fn, b.fnEnv, c.Args, e, sc)
			return
		}
		if fn, ok := o.(*types.Func); ok {
			g.funcCall(fn, c, e, sc)
		}
	case *ast.FuncLit:
		g.inlineLit(f, e, c.Args, e, sc)
	}
}

// body runs the body of fn with the given parameter bindings: -1 is the receiver, 0 and
// up the parameters. Parameters without a binding are fresh locals.
func (g *gen) body(fn *types.Func, binds map[int]*binding, sc *scope, ioName string) {
	decl, ok := g.p.funcs[fn.Origin()]
	if !ok {
		g.diag(sc, fn.Pos(), "no body for %s", fn.FullName())
		return
	}
	e := newEnv(nil)
	e.ioName = ioName
	if decl.Recv != nil && len(decl.Recv.List) > 0 && len(decl.Recv.List[0].Names) > 0 {
		if b, ok := binds[-1]; ok {
			if o := g.obj(decl.Recv.List[0].Names[0]); o != nil {
				e.vars[o] = b
			}
		}
	}
	i := 0
	for _, field := range decl.Type.Params.List {
		if len(field.Names) == 0 {
			i++
			continue
		}
		for _, n := range field.Names {
			if b, ok := binds[i]; ok {
				if o := g.obj(n); o != nil {
					e.vars[o] = b
				}
			}
			i++
		}
	}
	g.entered[decl.Body] = true
	g.stmts(decl.Body.List, e, sc)
}

// argBinding binds an argument of a call into a function that is run.
func (g *gen) argBinding(a ast.Expr, e *env) *binding {
	if g.isIO(a, e) {
		return &binding{io: true}
	}
	if fl, ok := unparen(a).(*ast.FuncLit); ok {
		return &binding{fn: fl, fnEnv: e}
	}
	if id, ok := unparen(a).(*ast.Ident); ok {
		if b := e.lookup(g.obj(id)); b != nil && b.fn != nil {
			return b
		}
	}
	if p, ok := g.place(a, e); ok {
		return &binding{place: p}
	}
	return nil
}

// inlineFunc runs fn as part of the current container.
func (g *gen) inlineFunc(fn *types.Func, recv ast.Expr, args []ast.Expr, e *env, sc *scope, ioName string) {
	if g.inlining[fn] {
		g.diag(sc, fn.Pos(), "recursive inlined function %s", fn.Name())
		return
	}
	g.inlining[fn] = true
	defer delete(g.inlining, fn)
	binds := map[int]*binding{}
	if recv != nil {
		if b := g.argBinding(recv, e); b != nil {
			binds[-1] = b
		}
	} else {
		// No receiver: a Reader method called on the IO (such as EventType), whose
		// receiver is the IO, or a function, for which body ignores this binding.
		binds[-1] = &binding{io: true}
	}
	for i, a := range args {
		if b := g.argBinding(a, e); b != nil {
			binds[i] = b
		}
	}
	g.body(fn, binds, sc, ioName)
}

// inlineLit runs a function literal as part of the current container.
func (g *gen) inlineLit(fl *ast.FuncLit, litEnv *env, args []ast.Expr, callEnv *env, sc *scope) {
	e := newEnv(litEnv)
	i := 0
	for _, field := range fl.Type.Params.List {
		for _, n := range field.Names {
			if i < len(args) {
				if b := g.argBinding(args[i], callEnv); b != nil {
					if o := g.obj(n); o != nil {
						e.vars[o] = b
					}
				}
			}
			i++
		}
		if len(field.Names) == 0 {
			i++
		}
	}
	g.entered[fl.Body] = true
	g.stmts(fl.Body.List, e, sc)
}

// funcValueType returns the type read by passing an element at place to the function
// value f (an IO method value, a function literal, a method value or a function).
func (g *gen) funcValueType(f ast.Expr, place string, e *env, sc *scope) any {
	f = unparen(f)
	switch x := f.(type) {
	case *ast.SelectorExpr:
		if g.isIO(x.X, e) {
			if t, ok := g.ioMethodType(x.Sel.Name, nil, sc, x.Pos()); ok {
				return t
			}
			g.diag(sc, x.Pos(), "IO method value %s", x.Sel.Name)
			return "void"
		}
		if sel, ok := g.p.info.Selections[x]; ok && sel.Kind() == types.MethodVal {
			fn, ok := sel.Obj().(*types.Func)
			if !ok {
				break
			}
			// A method of a context value, such as CommandEnumContext.Marshal(IO, *T).
			return g.namedBody(fn, typeName(fn), argumentBinds())
		}
		if fn, ok := g.p.info.Uses[x.Sel].(*types.Func); ok {
			return g.funcType(fn)
		}
	case *ast.Ident:
		o := g.obj(x)
		if b := e.lookup(o); b != nil && b.fn != nil {
			return g.collect(sc, func(nsc *scope) {
				g.inlineLitPlace(b.fn, b.fnEnv, place, nsc)
			})
		}
		if fn, ok := o.(*types.Func); ok {
			return g.funcType(fn)
		}
	case *ast.FuncLit:
		return g.collect(sc, func(nsc *scope) {
			g.inlineLitPlace(x, e, place, nsc)
		})
	}
	g.diag(sc, f.Pos(), "unsupported element function %T", f)
	return "void"
}

// inlineLitPlace runs a function literal whose first parameter that is not the IO
// is bound to place.
func (g *gen) inlineLitPlace(fl *ast.FuncLit, litEnv *env, place string, sc *scope) {
	e := newEnv(litEnv)
	bound := false
	for _, field := range fl.Type.Params.List {
		for _, n := range field.Names {
			o := g.obj(n)
			if o == nil {
				continue
			}
			if types.Identical(o.Type(), g.ioType) {
				e.vars[o] = &binding{io: true}
			} else if !bound {
				e.vars[o] = &binding{place: place}
				bound = true
			}
		}
	}
	g.entered[fl.Body] = true
	g.stmts(fl.Body.List, e, sc)
}
