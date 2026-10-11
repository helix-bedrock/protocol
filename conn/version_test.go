package conn

import (
	"testing"

	"github.com/helix-bedrock/protocol"
	"github.com/helix-bedrock/protocol/packet"
	"github.com/helix-bedrock/protocol/version"
)

// oldText is the text packet of a made-up old version: same payload, other ID.
type oldText struct{ packet.Text }

func (*oldText) ID() uint32 { return 250 }

// oldVersion is a made-up old version whose text packet has ID 250 and whose
// server splits every text message into two.
type oldVersion struct{ version.Protocol }

func (oldVersion) ID() int32 { return 1 }

func (o oldVersion) Packets(listener bool) packet.Pool {
	p := o.Protocol.Packets(listener)
	delete(p, packet.IDText)
	p[250] = func() packet.Packet { return &oldText{} }
	return p
}

func (oldVersion) ConvertToLatest(pk packet.Packet, s version.Session) []packet.Packet {
	if t, ok := pk.(*oldText); ok {
		// Prove the session is passed.
		t.SourceName = s.IdentityData().DisplayName
		return []packet.Packet{&t.Text}
	}
	return []packet.Packet{pk}
}

func (oldVersion) ConvertFromLatest(pk packet.Packet, _ version.Session) []packet.Packet {
	if t, ok := pk.(*packet.Text); ok {
		a, b := *t, *t
		a.Message, b.Message = "part 1: "+t.Message, "part 2: "+t.Message
		return []packet.Packet{&oldText{a}, &oldText{b}}
	}
	return []packet.Packet{pk}
}

func TestVersionConversion(t *testing.T) {
	old := oldVersion{version.Latest}
	a, b := memPair()
	// The client reads the old packets as they are (no conversion), so the
	// test sees exactly what is on the wire.
	client := New(a, Config{Pool: old.Packets(false)})
	srv := New(b, Config{Protocol: old})
	t.Cleanup(func() { _ = client.Close(); _ = srv.Close() })
	id := srv.IdentityData()
	id.DisplayName = "Alex"
	srv.SetLoginData(id, srv.ClientData())
	srv.SetGameData(version.GameData{Items: []protocol.ItemEntry{{Name: "minecraft:stone", RuntimeID: 1}}})
	if got := srv.GameData().Items; len(got) != 1 || got[0].Name != "minecraft:stone" {
		t.Fatalf("GameData().Items = %v", got)
	}

	// Server writes one latest packet; the client sees two old ones.
	send(t, srv, msg("hi"))
	for _, want := range []string{"part 1: hi", "part 2: hi"} {
		pk, err := client.ReadPacket()
		if err != nil {
			t.Fatalf("client read: %v", err)
		}
		if tx, ok := pk.(*oldText); !ok || tx.Message != want {
			t.Fatalf("client read %#v, want old text %q", pk, want)
		}
	}

	// Client writes an old packet; the server reads it as the latest one.
	if err := client.encode(&oldText{*msg("from old client")}); err != nil {
		t.Fatal(err)
	}
	if err := client.Flush(); err != nil {
		t.Fatal(err)
	}
	pk, err := srv.ReadPacket()
	if err != nil {
		t.Fatalf("server read: %v", err)
	}
	tx, ok := pk.(*packet.Text)
	if !ok || tx.Message != "from old client" || tx.SourceName != "Alex" {
		t.Fatalf("server read %#v, want latest text from Alex", pk)
	}
}

// cfb8Version is a made-up old version that uses CFB8 encryption and the old
// batch format.
type cfb8Version struct{ version.Protocol }

func (cfb8Version) Encryption(key [32]byte) packet.Encryption { return packet.NewCFB8Encryption(key) }
func (cfb8Version) LegacyNetworkSettings() packet.Compression { return packet.FlateCompression }

func TestLegacyTransport(t *testing.T) {
	old := cfb8Version{version.Latest}
	a, b := memPair()
	client := New(a, Config{Protocol: old, Pool: packet.NewServerPool()})
	srv := New(b, Config{Protocol: old})
	t.Cleanup(func() { _ = client.Close(); _ = srv.Close() })
	for _, c := range []*Conn{client, srv} {
		lp, ok := c.Protocol().(version.LegacyNetworkSettingsProtocol)
		if !ok {
			t.Fatal("protocol does not implement LegacyNetworkSettingsProtocol")
		}
		c.EnableLegacyCompression(lp.LegacyNetworkSettings())
		c.EnableEncryption([32]byte{7})
	}
	send(t, client, msg("old client"))
	send(t, client, msg("second"))
	for _, want := range []string{"old client", "second"} {
		if got := text(t, srv); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}
