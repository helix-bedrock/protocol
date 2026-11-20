package protodef

import (
	"go/ast"
	"go/constant"
	"go/token"
)

// fact is what is known about a value that is not read but set from constants.
type fact struct {
	// c is set for a value set to a constant.
	c constant.Value
	// d is set for a value set to a constant that depends on a field read before.
	d *derivation
}

// derivation says which constant a value holds for each value of a field read before,
// as code like this sets it:
//
//	*action = PlayerListActionRemove
//	if variant == 1 {
//		*action = PlayerListActionAdd
//	}
type derivation struct {
	src   string
	keys  []string
	table map[string]constant.Value
	// def is the value for a field value without a case, or nil when such a value is invalid.
	def constant.Value
}

// setConst records that the value at lhs was set to the constant v.
func (g *gen) setConst(lhs ast.Expr, v constant.Value, e *env, sc *scope) {
	p, ok := g.place(lhs, e)
	if !ok {
		return
	}
	sc.facts[p] = &fact{c: v}
	sc.places[p] = ""
}

func (g *gen) factOf(place string, sc *scope) *fact {
	for s := sc; s != nil; s = s.parent {
		if f, ok := s.facts[place]; ok {
			return f
		}
	}
	return nil
}

// derive records what the arms of a switch on the field at src, none of which reads
// anything, set from constants.
func (g *gen) derive(src string, arms []arm, results []*scope, sc *scope) {
	assigned := map[string]bool{}
	for _, r := range results {
		for p, f := range r.facts {
			if f != nil && f.c != nil {
				assigned[p] = true
			}
		}
	}
	for p := range assigned {
		d := &derivation{src: src, table: map[string]constant.Value{}}
		prior := g.factOf(p, sc)
		ok := true
		for i, a := range arms {
			r := results[i]
			if kindOf(r) == branchError {
				continue
			}
			var v constant.Value
			if f, set := r.facts[p]; set && f != nil && f.c != nil {
				v = f.c
			} else if prior != nil && prior.c != nil {
				v = prior.c
			}
			if v == nil {
				ok = false
				break
			}
			if a.isDefault {
				d.def = v
				continue
			}
			for _, k := range a.keys {
				if _, dup := d.table[k]; !dup {
					d.keys = append(d.keys, k)
					d.table[k] = v
				}
			}
		}
		if ok {
			sc.facts[p] = &fact{d: d}
			sc.places[p] = ""
		}
	}
}

// derivedTest turns place == v into a test on the field the value at place depends on.
func (g *gen) derivedTest(place string, v constant.Value, neq bool, sc *scope) (test, bool) {
	f := g.factOf(place, sc)
	if f == nil || f.d == nil {
		return test{}, false
	}
	d := f.d
	ref, ok := g.ref(d.src, sc)
	if !ok {
		return test{}, false
	}
	var match, other []string
	for _, k := range d.keys {
		if constant.Compare(d.table[k], token.EQL, v) {
			match = append(match, k)
		} else {
			other = append(other, k)
		}
	}
	if d.def != nil && constant.Compare(d.def, token.EQL, v) {
		// True for every value but those mapped to something else.
		return test{ref: ref, keys: other, neg: !neq}, true
	}
	return test{ref: ref, keys: match, neg: neq}, true
}

// recordArms notes the values a switch on the field at src accepts, when none of its
// arms reads anything and its default rejects every other value (as a string read and
// then turned into an enum does).
func (g *gen) recordArms(src string, arms []arm, results []*scope, sc *scope) {
	var keys []string
	for i, a := range arms {
		if a.isDefault {
			if kindOf(results[i]) != branchError {
				return
			}
			continue
		}
		if kindOf(results[i]) != branchError {
			keys = append(keys, a.keys...)
		}
	}
	g.recordValues(src, keys, sc)
}

// recordValues notes that the field at src only takes the values keys: the Reader
// rejects every other. ProtoDef cannot say so; the test uses it to make values the
// Reader accepts.
func (g *gen) recordValues(src string, keys []string, sc *scope) {
	if len(keys) == 0 || len(g.stack) == 0 {
		return
	}
	for _, k := range keys {
		if k == "true" || k == "false" {
			return
		}
	}
	// The test finds the field by its name in the type being built, wherever in the type
	// (and in whichever scope of the walk) it is.
	_, name, _ := sc.owner(src)
	if name == "" {
		return
	}
	typ := g.stack[len(g.stack)-1]
	if g.values[typ] == nil {
		g.values[typ] = map[string][]string{}
	}
	g.values[typ][name] = keys
}
