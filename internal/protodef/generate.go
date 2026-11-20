package protodef

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"slices"
	"sort"
)

// Packet is a packet found in the packet pool.
type Packet struct {
	// ID is the packet ID, as in mcpe_packet.
	ID uint32
	// Name is the snake_case name used in mcpe_packet; its type is "packet_" + Name.
	Name string
	// GoName is the name of the Go type in the packet package.
	GoName string
}

// Schema is a generated ProtoDef schema.
type Schema struct {
	// Types holds every type in the order written to protocol.json.
	Types *Map
	// Packets lists every packet in the pool, by ID.
	Packets []Packet
	// Values maps a type name, then a field name, to the only values the Reader accepts
	// for that field: strings, or numbers written in decimal. ProtoDef cannot say this,
	// so it is not in the JSON.
	Values map[string]map[string][]string
	// goFields maps a type name, then a field name, to the paths of the Go struct fields
	// the Reader reads it into (one per switch case), for fields read straight into one.
	// A path starting with ~ is a field of another integer type (IntegerFunc).
	goFields map[string]map[string][]string
	// Diagnostics lists the code the generator could not turn into ProtoDef. A schema
	// with diagnostics is incomplete where they point.
	Diagnostics []string
}

// MarshalJSON writes the schema in the layout of minecraft-data's protocol.json.
func (s *Schema) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"types": s.Types})
}

// Generate builds the schema from the protocol module in dir.
func Generate(dir string) (*Schema, error) {
	p, err := load(dir)
	if err != nil {
		return nil, err
	}
	ioObj, ok1 := p.root.Scope().Lookup("IO").(*types.TypeName)
	readerObj, ok2 := p.root.Scope().Lookup("Reader").(*types.TypeName)
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("protodef: protocol.IO or protocol.Reader not found")
	}
	g := &gen{
		p:         p,
		ioType:    ioObj.Type(),
		reader:    types.NewPointer(readerObj.Type()),
		named:     NewMap(),
		names:     map[any]string{},
		owners:    map[string]any{},
		lookups:   map[*types.Func]*lookupTable{},
		inlining:  map[*types.Func]bool{},
		values:    map[string]map[string][]string{},
		overrides: overrides(),
		visited:   map[*ast.CallExpr]bool{},
		entered:   map[*ast.BlockStmt]bool{},
		consts:    map[*Field]map[*types.Const]bool{},
		goFields:  map[string]map[string]*Field{},
	}
	for _, n := range declaredNatives() {
		g.owners[n] = n
	}
	al := aliases()
	for _, n := range al.Keys() {
		g.owners[n] = n
	}

	packets, err := g.packets()
	if err != nil {
		return nil, err
	}
	ids := NewMap()
	params := NewMap()
	packetTypes := NewMap()
	for _, pk := range packets {
		obj, _ := p.packet.Scope().Lookup(pk.GoName).(*types.TypeName)
		if obj == nil {
			return nil, fmt.Errorf("protodef: packet type %s not found", pk.GoName)
		}
		sel := types.NewMethodSet(types.NewPointer(obj.Type())).Lookup(p.packet, "Marshal")
		if sel == nil {
			return nil, fmt.Errorf("protodef: packet %s has no Marshal method", pk.GoName)
		}
		fn, ok := sel.Obj().(*types.Func)
		if !ok {
			return nil, fmt.Errorf("protodef: Marshal of packet %s is not a method", pk.GoName)
		}
		name := g.namedBody(fn, "packet_"+pk.Name, receiverBinds())
		ids.Set(fmt.Sprint(pk.ID), pk.Name)
		params.Set(pk.Name, name)
		packetTypes.Set(name, nil)
	}

	g.applyMappers()
	g.skippedCalls()

	out := NewMap()
	for _, n := range declaredNatives() {
		out.Set(n, "native")
	}
	for _, n := range al.Keys() {
		v, _ := al.Get(n)
		out.Set(n, v)
	}
	var shared []string
	for _, n := range g.named.Keys() {
		if _, isPacket := packetTypes.Get(n); !isPacket {
			shared = append(shared, n)
		}
	}
	sort.Strings(shared)
	for _, n := range shared {
		v, _ := g.named.Get(n)
		out.Set(n, v)
	}
	out.Set("mcpe_packet", container([]*Field{
		{Name: "name", Type: mapper("varint", ids)},
		{Name: "params", Type: switchOf("name", params, nil)},
	}))
	for _, n := range packetTypes.Keys() {
		v, _ := g.named.Get(n)
		out.Set(n, v)
	}

	diags := slices.Clone(g.diags)
	slices.Sort(diags)
	diags = slices.Compact(diags)
	goFields := map[string]map[string][]string{}
	for typ, fields := range g.goFields {
		goFields[typ] = map[string][]string{}
		for path, f := range fields {
			goFields[typ][f.Name] = append(goFields[typ][f.Name], path)
		}
		for _, paths := range goFields[typ] {
			slices.Sort(paths)
		}
	}
	return &Schema{Types: out, Packets: packets, Values: g.values, Diagnostics: diags, goFields: goFields}, nil
}

// packets reads the packet pool from the init function of the packet package: every
// map literal of IDs to func() Packet { return &T{} }.
func (g *gen) packets() ([]Packet, error) {
	byID := map[uint32]*Packet{}
	for _, f := range g.p.files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Name.Name != "init" || fd.Recv != nil || fd.Body == nil {
				continue
			}
			if g.p.info.Defs[fd.Name] == nil || g.p.info.Defs[fd.Name].Pkg() != g.p.packet {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
					return true
				}
				lit, ok := as.Rhs[0].(*ast.CompositeLit)
				if !ok {
					return true
				}
				for _, el := range lit.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					v := g.p.info.Types[kv.Key].Value
					if v == nil {
						continue
					}
					id64, ok := constant.Uint64Val(v)
					if !ok {
						continue
					}
					goName := packetTypeName(kv.Value)
					if goName == "" {
						// The packet would be missing from the schema.
						g.diags = append(g.diags, fmt.Sprintf("%s: packet pool entry not of the form func() Packet { return &T{} }", g.p.fset.Position(kv.Value.Pos())))
						continue
					}
					id := uint32(id64)
					if pk, seen := byID[id]; !seen {
						byID[id] = &Packet{ID: id, Name: snake(goName), GoName: goName}
					} else if pk.GoName != goName {
						g.diags = append(g.diags, fmt.Sprintf("%s: packet ID %d is %s in one pool and %s in another", g.p.fset.Position(kv.Pos()), id, pk.GoName, goName))
					}
				}
				return true
			})
		}
	}
	if len(byID) == 0 {
		return nil, fmt.Errorf("protodef: no packets found in the packet pool")
	}
	out := make([]Packet, 0, len(byID))
	for _, pk := range byID {
		out = append(out, *pk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// packetTypeName returns T from func() Packet { return &T{} }.
func packetTypeName(x ast.Expr) string {
	fl, ok := x.(*ast.FuncLit)
	if !ok || len(fl.Body.List) != 1 {
		return ""
	}
	ret, ok := fl.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return ""
	}
	u, ok := ret.Results[0].(*ast.UnaryExpr)
	if !ok || u.Op != token.AND {
		return ""
	}
	cl, ok := u.X.(*ast.CompositeLit)
	if !ok {
		return ""
	}
	if id, ok := cl.Type.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}
