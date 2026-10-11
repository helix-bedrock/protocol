package version

import (
	"slices"
	"testing"

	"github.com/helix-bedrock/protocol"
)

func TestLatest(t *testing.T) {
	if Latest.ID() != protocol.CurrentProtocol || Latest.Ver() != protocol.CurrentVersion {
		t.Fatalf("Latest = %v %v", Latest.ID(), Latest.Ver())
	}
	if len(Latest.Packets(true)) == 0 || len(Latest.Packets(false)) == 0 {
		t.Fatal("Latest has empty pools")
	}
}

type fake struct{ latest }

func (fake) ID() int32 { return 100 }

func TestSet(t *testing.T) {
	s := NewSet(fake{}, nil)
	if p, ok := s.Lookup(100); !ok || p.ID() != 100 {
		t.Fatal("Lookup(100) failed")
	}
	if _, ok := s.Lookup(protocol.CurrentProtocol); !ok {
		t.Fatal("Set does not hold Latest")
	}
	if _, ok := s.Lookup(1); ok {
		t.Fatal("Lookup(1) found an unknown protocol")
	}
	ids := s.IDs()
	slices.Sort(ids)
	if len(ids) != 2 || ids[0] != 100 {
		t.Fatalf("IDs = %v", ids)
	}
}
