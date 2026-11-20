package protodef

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
)

// Fields that a switch branches on get a mapper naming their values, as minecraft-data
// writes them: text_type is ["mapper", {"type": "u8", "mappings": {"0": "raw", ...}}].
// The names come from the Go constants the cases use, with the prefix their const block
// shares cut off (TextTypeChat in a block of TextType constants is "chat"). Switches are
// built with numeric keys, and a pass at the end turns the fields into mappers and the
// keys of every switch on them into names.

// switchUse is a switch on a field.
type switchUse struct {
	field  *Field
	fields *Map
}

// constOf returns the constant x names, looking through conversions such as uint32(C).
func (g *gen) constOf(x ast.Expr) *types.Const {
	switch x := unparen(x).(type) {
	case *ast.Ident:
		c, _ := g.p.info.Uses[x].(*types.Const)
		return c
	case *ast.SelectorExpr:
		c, _ := g.p.info.Uses[x.Sel].(*types.Const)
		return c
	case *ast.CallExpr:
		if tv, ok := g.p.info.Types[x.Fun]; ok && tv.IsType() && len(x.Args) == 1 {
			return g.constOf(x.Args[0])
		}
	}
	return nil
}

// fieldOf returns the field holding place, if it is a field of a container being built.
func (g *gen) fieldOf(place string, sc *scope) *Field {
	s, name, _ := sc.owner(place)
	if name == "" {
		return nil
	}
	for _, f := range s.fields {
		if f.Name == name && !f.Anon {
			return f
		}
	}
	return nil
}

// noteConst records that the field at place is compared with the constant c.
func (g *gen) noteConst(place string, c *types.Const, sc *scope) {
	if c == nil {
		return
	}
	if f := g.fieldOf(place, sc); f != nil {
		if g.consts[f] == nil {
			g.consts[f] = map[*types.Const]bool{}
		}
		g.consts[f][c] = true
	}
}

// noteSwitch records a switch on the field at place.
func (g *gen) noteSwitch(place string, fields *Map, sc *scope) {
	if f := g.fieldOf(place, sc); f != nil {
		g.uses = append(g.uses, switchUse{field: f, fields: fields})
	}
}

// constGroups maps every constant declared in a const block to the whole block.
func (g *gen) constGroups() map[*types.Const][]*types.Const {
	groups := map[*types.Const][]*types.Const{}
	for _, f := range g.p.files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			var block []*types.Const
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, n := range vs.Names {
					if c, ok := g.p.info.Defs[n].(*types.Const); ok && n.Name != "_" {
						block = append(block, c)
					}
				}
			}
			for _, c := range block {
				groups[c] = block
			}
		}
	}
	return groups
}

// isInteger reports whether a ProtoDef type is an integer a mapper can be put over.
func isInteger(t string) bool {
	switch t {
	case "u8", "i8", "lu16", "li16", "lu32", "li32", "i32", "varint", "zigzag32", "varint64", "zigzag64", "lu64", "li64":
		return true
	}
	return false
}

// applyMappers turns the fields compared with constants into mappers and renames the
// keys of every switch on them.
func (g *gen) applyMappers() {
	groups := g.constGroups()
	mappings := map[*Field]*Map{}
	for f, consts := range g.consts {
		native, ok := f.Type.(string)
		if !ok || !isInteger(native) {
			continue
		}
		m := NewMap()
		seen := map[string]bool{}
		for _, block := range uniqueBlocks(consts, groups) {
			prefix := commonPrefix(block)
			for _, c := range block {
				key := constKey(c.Val())
				if _, taken := m.Get(key); taken {
					continue
				}
				name := strings.Join(snakeWords(c.Name())[prefix:], "_")
				if name == "" || seen[name] {
					name = snake(c.Name())
				}
				if seen[name] {
					continue
				}
				seen[name] = true
				m.Set(key, name)
			}
		}
		if m.Len() > 0 {
			mappings[f] = m
		}
	}
	for f, m := range mappings {
		f.Type = mapper(f.Type, m)
	}
	for _, u := range g.uses {
		m, ok := mappings[u.field]
		if !ok {
			continue
		}
		renamed := NewMap()
		for _, k := range u.fields.Keys() {
			v, _ := u.fields.Get(k)
			if name, ok := m.Get(k); ok {
				k, _ = name.(string)
			}
			renamed.Set(k, v)
		}
		*u.fields = *renamed
	}
}

// uniqueBlocks returns the const blocks of consts, each once, in declaration order so
// that the schema does not depend on map iteration order.
func uniqueBlocks(consts map[*types.Const]bool, groups map[*types.Const][]*types.Const) [][]*types.Const {
	var blocks [][]*types.Const
	seen := map[*types.Const]bool{}
	for c := range consts {
		block := groups[c]
		if len(block) == 0 {
			block = []*types.Const{c}
		}
		if seen[block[0]] {
			continue
		}
		seen[block[0]] = true
		blocks = append(blocks, block)
	}
	sort.Slice(blocks, func(i, j int) bool {
		a, b := blocks[i][0], blocks[j][0]
		if a.Pkg() != b.Pkg() {
			return a.Pkg().Path() < b.Pkg().Path()
		}
		return a.Pos() < b.Pos()
	})
	return blocks
}

func snakeWords(name string) []string { return strings.Split(snake(name), "_") }

// commonPrefix returns how many leading words all names in block share, leaving at
// least one word of each name.
func commonPrefix(block []*types.Const) int {
	if len(block) < 2 {
		return 0
	}
	words := make([][]string, len(block))
	shortest := -1
	for i, c := range block {
		words[i] = snakeWords(c.Name())
		if shortest < 0 || len(words[i]) < shortest {
			shortest = len(words[i])
		}
	}
	for n := 0; n < shortest-1; n++ {
		for _, w := range words[1:] {
			if w[n] != words[0][n] {
				return n
			}
		}
	}
	return shortest - 1
}
