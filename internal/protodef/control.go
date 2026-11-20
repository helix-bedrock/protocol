package protodef

import (
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"sort"
	"strings"
)

// test is a condition on a field read before: true when the field holds one of keys,
// or, with neg, when it holds none of them.
type test struct {
	ref  string
	keys []string
	neg  bool
	// place is the Go value of the field.
	place string
}

// test turns a condition into a test on a field. It reports false for conditions on
// values that are not on the wire.
func (g *gen) test(x ast.Expr, e *env, sc *scope) (test, bool) {
	switch x := unparen(x).(type) {
	case *ast.UnaryExpr:
		if x.Op == token.NOT {
			t, ok := g.test(x.X, e, sc)
			t.neg = !t.neg
			return t, ok
		}
	case *ast.BinaryExpr:
		switch x.Op {
		case token.EQL, token.NEQ:
			val, other := x.Y, x.X
			if g.p.info.Types[val].Value == nil {
				val, other = x.X, x.Y
			}
			v := g.p.info.Types[val].Value
			if v == nil {
				return test{}, false
			}
			p, ok := g.place(other, e)
			if !ok {
				return test{}, false
			}
			if ref, ok := g.ref(p, sc); ok {
				g.noteConst(p, g.constOf(val), sc)
				return test{ref: ref, keys: []string{constKey(v)}, neg: x.Op == token.NEQ, place: p}, true
			}
			return g.derivedTest(p, v, x.Op == token.NEQ, sc)
		case token.LOR:
			a, ok1 := g.test(x.X, e, sc)
			b, ok2 := g.test(x.Y, e, sc)
			if ok1 && ok2 && a.ref == b.ref && !a.neg && !b.neg {
				return test{ref: a.ref, keys: append(a.keys, b.keys...), place: a.place}, true
			}
		case token.LAND:
			// a != x && a != y is the negation of a == x || a == y.
			a, ok1 := g.test(x.X, e, sc)
			b, ok2 := g.test(x.Y, e, sc)
			if ok1 && ok2 && a.ref == b.ref && a.neg && b.neg {
				return test{ref: a.ref, keys: append(a.keys, b.keys...), neg: true, place: a.place}, true
			}
		}
	case *ast.Ident, *ast.SelectorExpr:
		if t := g.p.info.TypeOf(x); t != nil {
			if b, ok := t.Underlying().(*types.Basic); ok && b.Info()&types.IsBoolean != 0 {
				if p, ok := g.place(x, e); ok {
					if ref, ok := g.ref(p, sc); ok {
						return test{ref: ref, keys: []string{"true"}, place: p}, true
					}
				}
			}
		}
	}
	return test{}, false
}

type branchKind int

const (
	branchVoid branchKind = iota
	branchError
	branchIO
)

func kindOf(sc *scope) branchKind {
	switch {
	case len(sc.fields) > 0:
		return branchIO
	case sc.errored:
		return branchError
	}
	return branchVoid
}

// child returns a scope for one branch of a switch inside sc. Its field names avoid the
// names in sc, as ProtoDef merges the fields of an anonymous switch into the parent.
func child(sc *scope, level bool) *scope {
	c := newScope(sc, level)
	c.used = maps.Clone(sc.used)
	return c
}

// mentionsField reports whether x uses a value read into a field before.
func (g *gen) mentionsField(x ast.Expr, e *env, sc *scope) bool {
	found := false
	ast.Inspect(x, func(n ast.Node) bool {
		if found {
			return false
		}
		switch n := n.(type) {
		case *ast.Ident, *ast.SelectorExpr:
			if p, ok := g.place(n.(ast.Expr), e); ok {
				if _, ok := g.ref(p, sc); ok {
					found = true
				}
			}
		case *ast.CallExpr:
			// A call such as len(x) or lookup(id, x) is not the value of a field.
			return false
		}
		return !found
	})
	return found
}

// dryRun runs list in a scope that is thrown away, to see what it does.
func (g *gen) dryRun(list []ast.Stmt, e *env, sc *scope) *scope {
	p := child(sc, true)
	g.stmts(list, newEnv(e), p)
	return p
}

// probe reports what list reads.
func (g *gen) probe(list []ast.Stmt, e *env, sc *scope) branchKind {
	if len(list) == 0 {
		return branchVoid
	}
	return kindOf(g.dryRun(list, e, sc))
}

// decidesType reports whether list records the concrete type of an interface value.
func (g *gen) decidesType(list []ast.Stmt, e *env, sc *scope) bool {
	return len(g.dryRun(list, e, sc).dyn) > 0
}

// containsReturn reports whether list has a return statement, outside function literals
// and outside statement lists that report invalid data (where the read stops anyway).
func containsReturn(list []ast.Stmt) bool {
	errors := false
	for _, s := range list {
		if es, ok := s.(*ast.ExprStmt); ok {
			if c, ok := es.X.(*ast.CallExpr); ok {
				if sel, ok := unparen(c.Fun).(*ast.SelectorExpr); ok && isIOError(sel.Sel.Name) {
					errors = true
				}
			}
		}
	}
	for _, s := range list {
		switch s := s.(type) {
		case *ast.ReturnStmt:
			if !errors {
				return true
			}
		case *ast.BlockStmt:
			if containsReturn(s.List) {
				return true
			}
		case *ast.IfStmt:
			if containsReturn(s.Body.List) || s.Else != nil && containsReturn([]ast.Stmt{s.Else}) {
				return true
			}
		case *ast.SwitchStmt:
			for _, c := range s.Body.List {
				if cc, ok := c.(*ast.CaseClause); ok && containsReturn(cc.Body) {
					return true
				}
			}
		case *ast.ForStmt:
			if containsReturn(s.Body.List) {
				return true
			}
		case *ast.RangeStmt:
			if containsReturn(s.Body.List) {
				return true
			}
		}
	}
	return false
}

func terminates(list []ast.Stmt) bool {
	if len(list) == 0 {
		return false
	}
	return terminatesStmt(list[len(list)-1])
}

func terminatesStmt(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.BlockStmt:
		return terminates(s.List)
	case *ast.IfStmt:
		return s.Else != nil && terminates(s.Body.List) && terminatesStmt(s.Else)
	}
	return false
}

// ifStmt runs an if statement. When the branch taken ends in a return and there is no
// else, the rest of the list is the else branch: consumed reports that.
func (g *gen) ifStmt(s *ast.IfStmt, rest []ast.Stmt, e *env, sc *scope) (consumed, term bool) {
	if s.Init != nil {
		g.stmt(s.Init, e, sc)
	}
	g.exprCalls(s.Cond, e, sc)
	thenList := s.Body.List
	var elseList []ast.Stmt
	if s.Else != nil {
		elseList = []ast.Stmt{s.Else}
	} else if terminates(thenList) {
		elseList, consumed = rest, true
	}
	term = terminates(thenList) && (consumed && terminates(rest) || s.Else != nil && terminatesStmt(s.Else))

	thenKind, elseKind := g.probe(thenList, e, sc), g.probe(elseList, e, sc)
	// Only a branch that ends in a return ends the function: a return deeper inside a
	// branch would make the statements after the if conditional, which is not modelled.
	if thenKind != branchError && !terminates(thenList) && containsReturn(thenList) ||
		s.Else != nil && elseKind != branchError && !terminatesStmt(s.Else) && containsReturn(elseList) {
		g.diag(sc, s.Pos(), "return inside a branch that does not end in one")
	}
	t, ok := g.test(s.Cond, e, sc)
	if !ok && thenKind != branchIO && g.decidesType(thenList, e, sc) {
		// A branch on a value not on the wire that only picks the concrete type of an
		// interface value, such as descriptor, found = itemDescriptorFromType(variant):
		// keep what it picks, and read the rest as usual.
		g.stmts(thenList, e, sc)
		g.stmts(elseList, e, sc)
		return consumed, term
	}
	arms := []arm{{keys: t.keys, list: thenList}, {isDefault: true, list: elseList}}
	if t.neg {
		arms = []arm{{keys: t.keys, list: elseList}, {isDefault: true, list: thenList}}
	}
	switch {
	case thenKind != branchIO && elseKind != branchIO && thenKind != branchError && elseKind != branchError:
		// Nothing is read either way. A branch on a field read before may still set a
		// value from it; any other branch is a check of values already read.
		if ok {
			g.emitArms(t.ref, t.place, arms, e, sc)
		} else {
			// Run them anyway for what they tell of the values read before, such as
			// if v, ok := x.Value(); ok { fromString(v) }; the scopes are thrown away.
			g.dryRun(thenList, e, sc)
			g.dryRun(elseList, e, sc)
		}
		return consumed, term
	case thenKind == branchError:
		// if x != 16 { error }: x must be 16.
		if ok && t.neg {
			g.recordValues(t.place, t.keys, sc)
		}
		g.stmts(elseList, e, sc)
		return consumed, term
	case elseKind == branchError:
		if ok && !t.neg {
			g.recordValues(t.place, t.keys, sc)
		}
		g.stmts(thenList, e, sc)
		return consumed, term
	}
	if !ok {
		if thenKind != branchIO {
			if g.mentionsField(s.Cond, e, sc) {
				// Such as flags&mask != 0 or x < 10: ProtoDef cannot branch on it.
				g.diag(sc, s.Pos(), "condition %s on a field read before is not a comparison with constants", types.ExprString(s.Cond))
			}
			// A guard on a value that is not on the wire, such as a nil check.
			g.stmts(elseList, e, sc)
			return consumed, term
		}
		g.diag(sc, s.Pos(), "condition %s is not on a field read before", types.ExprString(s.Cond))
		g.stmts(thenList, e, sc)
		return consumed, term
	}
	g.emitArms(t.ref, t.place, arms, e, sc)
	return consumed, term
}

type arm struct {
	keys      []string
	isDefault bool
	list      []ast.Stmt
}

// emitArms adds a switch on ref with one case per arm.
func (g *gen) emitArms(ref, place string, arms []arm, e *env, sc *scope) {
	results := make([]*scope, len(arms))
	anyIO := false
	for i, a := range arms {
		results[i] = child(sc, true)
		g.stmts(a.list, newEnv(e), results[i])
		if kindOf(results[i]) == branchIO {
			anyIO = true
		}
	}
	if !anyIO {
		for _, r := range results {
			sc.diags = append(sc.diags, r.diags...)
			maps.Copy(sc.dyn, r.dyn)
		}
		if place != "" {
			g.derive(place, arms, results, sc)
			g.recordArms(place, arms, results, sc)
		}
		return
	}
	// When every case that reads something reads one field of the same name, the switch
	// is that field's type, as minecraft-data writes it.
	single := ""
	for _, r := range results {
		if kindOf(r) != branchIO {
			continue
		}
		if len(r.fields) != 1 || r.fields[0].Anon || (single != "" && r.fields[0].Name != single) {
			single = "-"
			break
		}
		single = r.fields[0].Name
	}
	if single != "-" {
		for i, a := range arms {
			if kindOf(results[i]) == branchIO {
				results[i] = child(sc, false)
				g.stmts(a.list, newEnv(e), results[i])
				if len(results[i].fields) != 1 {
					g.diag(sc, a.list[0].Pos(), "case read a different number of fields the second time")
					return
				}
			}
		}
	}
	fields := NewMap()
	var def any = "void"
	for i, a := range arms {
		r := results[i]
		var typ any = "void"
		if kindOf(r) == branchIO {
			if single != "-" {
				typ = r.fields[0].Type
			} else {
				typ = container(r.fields)
			}
		}
		if a.isDefault {
			if kindOf(r) == branchError {
				def = nil
			} else {
				def = typ
			}
			continue
		}
		if kindOf(r) == branchError {
			continue
		}
		for _, k := range a.keys {
			if _, ok := fields.Get(k); !ok {
				fields.Set(k, typ)
			}
		}
	}
	g.noteSwitch(place, fields, sc)
	sw := switchOf(ref, fields, def)
	// The cases took their names from a copy of the parent's counts: take theirs, so a
	// later field does not get one of the same names.
	for _, r := range results {
		for n := range r.used {
			sc.used[n] = max(sc.used[n], r.used[n])
		}
	}
	if single != "-" {
		// The cases named the field from the parent's counts, so the name is free here.
		name := single
		sc.used[name] = max(sc.used[name], 1)
		sc.fields = append(sc.fields, &Field{Name: name, Type: sw})
		for _, r := range results {
			sc.commit(r, func(string) string { return name })
		}
		return
	}
	sc.fields = append(sc.fields, &Field{Anon: true, Type: sw})
	for _, r := range results {
		sc.commit(r, nil)
	}
}

func (g *gen) switchStmt(s *ast.SwitchStmt, e *env, sc *scope) {
	if s.Init != nil {
		g.stmt(s.Init, e, sc)
	}
	if s.Tag == nil {
		g.stmt(ifChain(s.Body.List), e, sc)
		return
	}
	g.exprCalls(s.Tag, e, sc)
	var arms []arm
	var consts []*types.Const
	hasDefault := false
	for _, c := range s.Body.List {
		cc, ok := c.(*ast.CaseClause)
		if !ok {
			continue
		}
		a := arm{list: cc.Body, isDefault: cc.List == nil}
		hasDefault = hasDefault || a.isDefault
		for _, k := range cc.List {
			v := g.p.info.Types[k].Value
			if v == nil {
				g.diag(sc, k.Pos(), "case %s is not a constant", types.ExprString(k))
				continue
			}
			a.keys = append(a.keys, constKey(v))
			consts = append(consts, g.constOf(k))
		}
		arms = append(arms, a)
	}
	if !hasDefault {
		arms = append(arms, arm{isDefault: true})
	}
	for _, a := range arms {
		if containsReturn(a.list) && g.probe(a.list, e, sc) != branchError {
			g.diag(sc, s.Pos(), "return inside a switch case that does not report an error")
			break
		}
	}
	place, _ := g.place(s.Tag, e)
	ref, ok := g.ref(place, sc)
	if ok {
		for _, c := range consts {
			g.noteConst(place, c, sc)
		}
	}
	if !ok {
		for _, a := range arms {
			if g.probe(a.list, e, sc) == branchIO {
				g.diag(sc, s.Pos(), "switch on %s, which is not a field read before", types.ExprString(s.Tag))
				return
			}
		}
		return
	}
	g.emitArms(ref, place, arms, e, sc)
}

// ifChain turns the clauses of a switch without a tag into if/else statements.
func ifChain(clauses []ast.Stmt) ast.Stmt {
	var def []ast.Stmt
	var conds []*ast.CaseClause
	for _, c := range clauses {
		cc, ok := c.(*ast.CaseClause)
		if !ok {
			continue
		}
		if cc.List == nil {
			def = cc.Body
			continue
		}
		conds = append(conds, cc)
	}
	var out ast.Stmt = &ast.BlockStmt{List: def}
	for i := len(conds) - 1; i >= 0; i-- {
		cc := conds[i]
		cond := cc.List[0]
		for _, x := range cc.List[1:] {
			cond = &ast.BinaryExpr{X: cond, Op: token.LOR, Y: x, OpPos: x.Pos()}
		}
		out = &ast.IfStmt{If: cc.Pos(), Cond: cond, Body: &ast.BlockStmt{List: cc.Body}, Else: out}
	}
	return out
}

func (g *gen) forStmt(s *ast.ForStmt, e *env, sc *scope) {
	inner := newEnv(e)
	if s.Init != nil {
		g.stmt(s.Init, inner, sc)
	}
	if g.probe(s.Body.List, inner, sc) != branchIO {
		return
	}
	limit := loopLimit(s.Cond)
	if limit == nil {
		g.diag(sc, s.Pos(), "loop condition %s", types.ExprString(s.Cond))
		return
	}
	count, ok := g.countRef(limit, inner, sc)
	if !ok {
		g.diag(sc, s.Pos(), "loop count %s is not a field read before", types.ExprString(limit))
		return
	}
	g.loop(count, s.Body.List, inner, sc)
}

// loopLimit returns N from a loop condition i < N, ignoring checks of the reader's error.
func loopLimit(cond ast.Expr) ast.Expr {
	if c, ok := unparen(cond).(*ast.BinaryExpr); ok {
		switch c.Op {
		case token.LAND:
			if mentionsErr(c.Y) {
				return loopLimit(c.X)
			}
			if mentionsErr(c.X) {
				return loopLimit(c.Y)
			}
		case token.LSS:
			return c.Y
		}
	}
	return nil
}

func mentionsErr(x ast.Expr) bool {
	found := false
	ast.Inspect(x, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && (id.Name == "err" || id.Name == "Err") {
			found = true
		}
		return !found
	})
	return found
}

func (g *gen) rangeStmt(s *ast.RangeStmt, e *env, sc *scope) {
	inner := newEnv(e)
	var count any
	t := g.p.info.TypeOf(s.X)
	if v := g.p.info.Types[s.X].Value; v != nil {
		count, _ = g.countRef(s.X, e, sc)
	} else if t != nil {
		u := t.Underlying()
		if p, ok := u.(*types.Pointer); ok {
			u = p.Elem().Underlying()
		}
		if a, ok := u.(*types.Array); ok {
			count = a.Len()
		}
	}
	if s.Value != nil {
		if id, ok := s.Value.(*ast.Ident); ok && id.Name != "_" {
			if p, ok := g.place(s.X, e); ok {
				if o := g.obj(id); o != nil {
					inner.vars[o] = &binding{place: p + "[]"}
				}
			}
		}
	}
	if g.probe(s.Body.List, inner, sc) != branchIO {
		return
	}
	if count == nil {
		g.diag(sc, s.Pos(), "range over %s, which has no fixed length", types.ExprString(s.X))
		return
	}
	g.loop(count, s.Body.List, inner, sc)
}

// loop adds an array of count elements, each read by body.
func (g *gen) loop(count any, body []ast.Stmt, e *env, sc *scope) {
	elem := g.collect(sc, func(nsc *scope) {
		g.stmts(body, newEnv(e), nsc)
	})
	// Name the array after the slice the body reads into, if there is one.
	name := "entries"
	probe := child(sc, true)
	g.stmts(body, newEnv(e), probe)
	var slices []string
	for p := range probe.places {
		if i := strings.Index(p, "[]"); i >= 0 {
			slices = append(slices, p[:i])
		}
	}
	if len(slices) > 0 {
		// Sorted, as map order would make the name change from run to run.
		sort.Strings(slices)
		name = placeName(slices[0])
	}
	sc.fields = append(sc.fields, &Field{Name: sc.uniq(name), Type: arrayCount(count, elem)})
}
