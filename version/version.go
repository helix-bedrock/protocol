// Package version lets one server talk to clients on many Minecraft versions.
//
// Helix always works with the latest packets (the packet package). A Protocol
// describes one older version: its packet IDs, how its packets are read and
// written, and how to convert its packets to and from the latest ones. The
// conn package uses the Protocol a client logged in with, so the rest of the
// server never sees the difference.
//
// The interface matches gophertunnel's minecraft.Protocol, so adapters such
// as go-multiversion's can be ported with few changes.
package version

import (
	"io"

	"github.com/helix-bedrock/protocol"
	"github.com/helix-bedrock/protocol/login"
	"github.com/helix-bedrock/protocol/packet"
)

// ByteReader is the source a Protocol's reader reads from.
type ByteReader interface {
	io.Reader
	io.ByteReader
}

// ByteWriter is the destination a Protocol's writer writes to.
type ByteWriter interface {
	io.Writer
	io.ByteWriter
}

// Session is what a Protocol may know about the connection it converts
// packets for. A conversion may type-assert it to a larger interface, such as
// one with WritePacket, when it needs more.
type Session interface {
	// ClientData is the client data the player logged in with.
	ClientData() login.ClientData
	// IdentityData is the identity the player logged in with.
	IdentityData() login.IdentityData
}

// Protocol is one Minecraft protocol version.
type Protocol interface {
	// ID returns the protocol number, such as 2193.
	ID() int32
	// Ver returns the Minecraft version, such as "1.26.50".
	Ver() string
	// Packets returns the packets of this version. If listener is true, it
	// returns the packets a client may send, which is what a server reads.
	Packets(listener bool) packet.Pool
	// NewReader returns a reader for this version's wire format.
	NewReader(r ByteReader, shieldID int32, enableLimits bool) protocol.IO
	// NewWriter returns a writer for this version's wire format.
	NewWriter(w ByteWriter, shieldID int32) protocol.IO
	// ConvertToLatest converts a packet read from the client into zero or more
	// latest packets. It returns []packet.Packet{pk} if nothing changed.
	ConvertToLatest(pk packet.Packet, s Session) []packet.Packet
	// ConvertFromLatest converts a latest packet into zero or more packets of
	// this version. It returns []packet.Packet{pk} if nothing changed.
	ConvertFromLatest(pk packet.Packet, s Session) []packet.Packet
}

// Latest is the version the packet package implements. It converts nothing.
var Latest Protocol = latest{}

type latest struct{}

func (latest) ID() int32   { return protocol.CurrentProtocol }
func (latest) Ver() string { return protocol.CurrentVersion }

func (latest) Packets(listener bool) packet.Pool {
	if listener {
		return packet.NewClientPool()
	}
	return packet.NewServerPool()
}

func (latest) NewReader(r ByteReader, shieldID int32, enableLimits bool) protocol.IO {
	return protocol.NewReader(r, shieldID, enableLimits)
}

func (latest) NewWriter(w ByteWriter, shieldID int32) protocol.IO {
	return protocol.NewWriter(w, shieldID)
}

func (latest) ConvertToLatest(pk packet.Packet, _ Session) []packet.Packet {
	return []packet.Packet{pk}
}

func (latest) ConvertFromLatest(pk packet.Packet, _ Session) []packet.Packet {
	return []packet.Packet{pk}
}

// Set is a group of protocols a server accepts, looked up by ID.
type Set struct {
	byID map[int32]Protocol
}

// NewSet returns a Set holding Latest and the protocols passed. A later
// protocol with the same ID replaces an earlier one.
func NewSet(protocols ...Protocol) *Set {
	s := &Set{byID: map[int32]Protocol{Latest.ID(): Latest}}
	for _, p := range protocols {
		if p != nil {
			s.byID[p.ID()] = p
		}
	}
	return s
}

// Lookup returns the protocol with the ID a client sent in RequestNetworkSettings
// or Login, and false if the server does not accept it.
func (s *Set) Lookup(id int32) (Protocol, bool) {
	p, ok := s.byID[id]
	return p, ok
}

// IDs returns every accepted protocol ID.
func (s *Set) IDs() []int32 {
	ids := make([]int32, 0, len(s.byID))
	for id := range s.byID {
		ids = append(ids, id)
	}
	return ids
}
