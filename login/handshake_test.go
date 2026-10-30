package login

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// TestServerHandshake checks the token the way a client reads it: the x5u
// key verifies the signature, and the salt with the client's own ECDH half
// gives the same key as the server's.
func TestServerHandshake(t *testing.T) {
	server, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	client, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	token, key, err := ServerHandshake(server, &client.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwt.ParseSigned(string(token), []jose.SignatureAlgorithm{jose.ES384})
	if err != nil {
		t.Fatal(err)
	}
	x5u, _ := tok.Headers[0].ExtraHeaders["x5u"].(string)
	pub := &ecdsa.PublicKey{}
	if err := ParsePublicKey(x5u, pub); err != nil || !pub.Equal(&server.PublicKey) {
		t.Fatalf("x5u: %v", err)
	}
	var claims struct {
		Salt string `json:"salt"`
	}
	if err := tok.Claims(pub, &claims); err != nil {
		t.Fatalf("signature: %v", err)
	}
	if len(claims.Salt) != 24 || claims.Salt[22:] != "==" {
		t.Fatalf("salt %q is not 16 bytes in padded base64", claims.Salt)
	}
	salt, _ := base64.StdEncoding.DecodeString(claims.Salt)
	cpriv, _ := client.ECDH()
	spub, _ := pub.ECDH()
	secret, _ := cpriv.ECDH(spub)
	if sha256.Sum256(append(salt, secret...)) != key {
		t.Fatal("client and server keys differ")
	}
}

func TestServerHandshakeRejectsOtherCurves(t *testing.T) {
	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if _, _, err := ServerHandshake(p256, &p384.PublicKey); err == nil {
		t.Fatal("P-256 server key accepted")
	}
	if _, _, err := ServerHandshake(p384, nil); err == nil {
		t.Fatal("nil client key accepted")
	}
}

func BenchmarkServerHandshake(b *testing.B) {
	server, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	client, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := ServerHandshake(server, &client.PublicKey); err != nil {
			b.Fatal(err)
		}
	}
}
