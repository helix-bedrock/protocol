package conn

import (
	"github.com/helix-bedrock/protocol/internal/hook"
	"github.com/helix-bedrock/protocol/packet"
)

// Verdict is returned by hooks that may stop what they observe.
type Verdict = hook.Verdict

const (
	// Allow lets the packet continue. It is the zero value.
	Allow = hook.Allow
	// Drop stops the packet. Later hooks are not called.
	Drop = hook.Drop
)

// RawEvent describes one packet as raw bytes: its header and payload.
type RawEvent struct {
	// Conn is the connection the packet belongs to.
	Conn *Conn
	// Data is the packet. Incoming data is only valid during the hook. A hook
	// may replace it to change what is read or sent.
	Data []byte
}

// PacketEvent describes one decoded packet.
type PacketEvent struct {
	// Conn is the connection the packet belongs to.
	Conn *Conn
	// Header is the packet header. For outgoing packets only PacketID is set.
	Header packet.Header
	// Packet is the packet. A hook may change its fields or replace it.
	Packet packet.Packet
}

// Hooks are hooks on packets. Every Conn has its own, and Config.Shared runs
// for every Conn made with that Config. Each On method returns a function
// that removes the hook. Hooks cost nothing when none are registered, and a
// hook that panics is logged and ignored.
type Hooks struct {
	rawIn, rawOut       hook.List[func(*RawEvent) Verdict]
	packetIn, packetOut hook.List[func(*PacketEvent) Verdict]
	close               hook.List[func(*Conn)]
}

// OnRawIn registers fn for every packet received, as raw bytes, before it is
// decoded. Returning Drop discards it.
func (h *Hooks) OnRawIn(fn func(*RawEvent) Verdict) (remove func()) { return h.rawIn.Add(fn) }

// OnRawOut registers fn for every packet sent, as raw bytes, before it is
// batched. Returning Drop discards it.
func (h *Hooks) OnRawOut(fn func(*RawEvent) Verdict) (remove func()) { return h.rawOut.Add(fn) }

// OnPacketIn registers fn for every packet received, after it is decoded.
// Returning Drop discards it.
func (h *Hooks) OnPacketIn(fn func(*PacketEvent) Verdict) (remove func()) {
	return h.packetIn.Add(fn)
}

// OnPacketOut registers fn for every packet sent with WritePacket, before it
// is encoded. Returning Drop discards it.
func (h *Hooks) OnPacketOut(fn func(*PacketEvent) Verdict) (remove func()) {
	return h.packetOut.Add(fn)
}

// OnClose registers fn to be called once when the connection closes.
func (h *Hooks) OnClose(fn func(*Conn)) (remove func()) { return h.close.Add(fn) }

// Hooks returns the hooks of this connection only.
func (c *Conn) Hooks() *Hooks { return &c.hooks }

// runRaw runs raw hooks. It returns the (possibly replaced) data and false if
// a hook dropped it.
func (c *Conn) runRaw(in bool, b []byte) ([]byte, bool) {
	var s, own []hook.Entry[func(*RawEvent) Verdict]
	if in {
		s, own = c.shared.rawInList(), c.hooks.rawIn.Get()
	} else {
		s, own = c.shared.rawOutList(), c.hooks.rawOut.Get()
	}
	if s == nil && own == nil {
		return b, true
	}
	e := &RawEvent{Conn: c, Data: b}
	if hook.Run(c.log, e, s, own) == Drop {
		return nil, false
	}
	return e.Data, true
}

// runPacket runs packet hooks on *pk and stores a replaced packet back. It
// returns false if a hook dropped it.
func (c *Conn) runPacket(in bool, h packet.Header, pk *packet.Packet) bool {
	var s, own []hook.Entry[func(*PacketEvent) Verdict]
	if in {
		s, own = c.shared.packetInList(), c.hooks.packetIn.Get()
	} else {
		s, own = c.shared.packetOutList(), c.hooks.packetOut.Get()
	}
	if s == nil && own == nil {
		return true
	}
	e := &PacketEvent{Conn: c, Header: h, Packet: *pk}
	if hook.Run(c.log, e, s, own) == Drop || e.Packet == nil {
		return false
	}
	*pk = e.Packet
	return true
}

func (c *Conn) runClose() {
	var s []hook.Entry[func(*Conn)]
	if c.shared != nil {
		s = c.shared.close.Get()
	}
	own := c.hooks.close.Get()
	if s == nil && own == nil {
		return
	}
	hook.Observe(c.log, c, s, own)
}

// The list getters accept a nil *Hooks so a Conn without shared hooks needs
// no checks.

func (h *Hooks) rawInList() []hook.Entry[func(*RawEvent) Verdict] {
	if h == nil {
		return nil
	}
	return h.rawIn.Get()
}

func (h *Hooks) rawOutList() []hook.Entry[func(*RawEvent) Verdict] {
	if h == nil {
		return nil
	}
	return h.rawOut.Get()
}

func (h *Hooks) packetInList() []hook.Entry[func(*PacketEvent) Verdict] {
	if h == nil {
		return nil
	}
	return h.packetIn.Get()
}

func (h *Hooks) packetOutList() []hook.Entry[func(*PacketEvent) Verdict] {
	if h == nil {
		return nil
	}
	return h.packetOut.Get()
}
