package conn

import (
	"testing"
	"time"

	"github.com/helix-bedrock/protocol/packet"
	"github.com/helix-bedrock/raknet"
)

// TestOverRakNet sends packets over a real Helix raknet connection.
func TestOverRakNet(t *testing.T) {
	l, err := raknet.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	accepted := make(chan *raknet.Conn, 1)
	go func() {
		if c, err := l.Accept(); err == nil {
			accepted <- c.(*raknet.Conn)
		}
	}()
	rc, err := raknet.DialTimeout(l.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	var sc *raknet.Conn
	select {
	case sc = <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("accept timed out")
	}
	client := New(rc, Config{Pool: packet.NewServerPool()})
	srv := New(sc, Config{})
	t.Cleanup(func() { _ = client.Close(); _ = srv.Close() })
	for _, c := range []*Conn{client, srv} {
		c.EnableCompression(packet.FlateCompression, 256)
		c.EnableEncryption([32]byte{9})
	}
	send(t, client, msg("over raknet"))
	if got := text(t, srv); got != "over raknet" {
		t.Fatalf("got %q", got)
	}
}
