package protodef

import (
	"reflect"
	"testing"

	"github.com/helix-bedrock/protocol"
	"github.com/helix-bedrock/protocol/packet"
)

// TestPacketsMatchPools checks that the packets read from the pool's source are the
// packets the pools hold, so a pool entry the generator does not understand cannot drop
// a packet from the schema.
func TestPacketsMatchPools(t *testing.T) {
	s, _ := testSchema(t)
	pool := map[uint32]func() packet.Packet{}
	for _, p := range []packet.Pool{packet.NewServerPool(), packet.NewClientPool()} {
		for id, f := range p {
			pool[id] = f
		}
	}
	seen := map[uint32]bool{}
	for _, pk := range s.Packets {
		seen[pk.ID] = true
		f, ok := pool[pk.ID]
		if !ok {
			t.Errorf("packet %d (%s) is not in any pool", pk.ID, pk.GoName)
			continue
		}
		if name := reflect.TypeOf(f()).Elem().Name(); name != pk.GoName {
			t.Errorf("packet %d is %s in the pool and %s in the schema", pk.ID, name, pk.GoName)
		}
	}
	for id, f := range pool {
		if !seen[id] {
			t.Errorf("packet %d (%T) of the pool is not in the schema", id, f())
		}
	}
}

// TestIOMethodsClassified checks that the generator knows what every method of
// protocol.IO reads: a fixed type, a hand-written override, nothing (a check), part of
// the caller's container, or a type it follows in the Reader that reads something.
func TestIOMethodsClassified(t *testing.T) {
	s, _ := testSchema(t)
	ov := overrides()
	io := reflect.TypeOf((*protocol.IO)(nil)).Elem()
	for i := range io.NumMethod() {
		name := io.Method(i).Name
		if _, ok := ioPrimitive(name); ok {
			continue
		}
		if _, ok := ov[name]; ok {
			continue
		}
		if _, ok := ioInline(name); ok || isIOIgnored(name) || name == "NBT" || name == "NBTList" {
			continue
		}
		def, ok := s.Types.Get(name)
		if !ok {
			t.Errorf("IO.%s is not used by any packet and not classified: add it to ioPrimitive, overrides or isIOIgnored", name)
			continue
		}
		c, _ := def.([]any)
		if len(c) != 2 {
			t.Errorf("IO.%s is not a container type", name)
			continue
		}
		if fields, ok := c[1].([]*Field); !ok || len(fields) == 0 {
			t.Errorf("IO.%s reads nothing the generator follows", name)
		}
	}
	for name := range ov {
		if _, ok := io.MethodByName(name); !ok {
			t.Errorf("override %s is not a method of protocol.IO", name)
		}
	}
}
