package login

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
)

func testKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func testIdentity() IdentityData {
	return IdentityData{Identity: uuid.NewString(), DisplayName: "Steve"}
}

func testClientData() ClientData {
	return ClientData{DeviceOS: 7, GameVersion: "1.26.50", LanguageCode: "en_US", ServerAddress: "127.0.0.1:19132", SkinID: "steve",
		SkinResourcePatch: base64.StdEncoding.EncodeToString([]byte(`{"geometry":{"default":"geometry.humanoid.custom"}}`))}
}

// selfSignedToken builds a multiplayer token signed by key, as offline mode
// clients do.
func selfSignedToken(t testing.TB, key *ecdsa.PrivateKey, id IdentityData) string {
	t.Helper()
	keyData := MarshalPublicKey(&key.PublicKey)
	signer, err := jose.NewSigner(jose.SigningKey{Key: key, Algorithm: jose.ES384}, &jose.SignerOptions{
		ExtraHeaders: map[jose.HeaderKey]any{"x5u": keyData},
	})
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	tok, err := jwt.Signed(signer).Claims(tokenClaims{
		Claims: jwt.Claims{
			Expiry:    jwt.NewNumericDate(time.Now().Add(time.Hour)),
			NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Hour)),
			Audience:  jwt.Audience{"api://auth-minecraft-services/multiplayer"},
		},
		ClientPublicKey: keyData,
		DisplayName:     id.DisplayName,
		Identity:        id.Identity,
	}).Serialize()
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return tok
}

func TestTokenRequestRoundTrip(t *testing.T) {
	key := testKey(t)
	id := testIdentity()
	req, err := EncodeToken(selfSignedToken(t, key, id), testClientData(), key)
	if err != nil {
		t.Fatalf("EncodeToken: %v", err)
	}
	gotID, gotData, res, err := ParseWithConfig(req, Config{})
	if err != nil {
		t.Fatalf("ParseWithConfig: %v", err)
	}
	if gotID.DisplayName != "Steve" || gotID.Identity != id.Identity {
		t.Fatalf("identity = %+v", gotID)
	}
	if gotData.GameVersion != "1.26.50" {
		t.Fatalf("client data = %+v", gotData)
	}
	if res.Method != MethodToken || res.SentLegacyChain || res.XBOXLiveAuthenticated {
		t.Fatalf("result = %+v, want token, no chain, not authenticated", res)
	}
	if !res.PublicKey.Equal(&key.PublicKey) {
		t.Fatal("public key does not match")
	}
}

func TestLegacyChainRefusedByDefault(t *testing.T) {
	key := testKey(t)
	req := EncodeOffline(testIdentity(), testClientData(), key, true)
	if _, _, _, err := ParseWithConfig(req, Config{}); !errors.Is(err, ErrLegacyChain) {
		t.Fatalf("err = %v, want ErrLegacyChain", err)
	}
	_, _, res, err := ParseWithConfig(req, Config{AllowLegacyChain: true})
	if err != nil {
		t.Fatalf("with AllowLegacyChain: %v", err)
	}
	if res.Method != MethodLegacyChain || !res.SentLegacyChain {
		t.Fatalf("result = %+v, want legacy chain", res)
	}
}

func TestTokenWithChainIsFlagged(t *testing.T) {
	key := testKey(t)
	legacy := EncodeOffline(testIdentity(), testClientData(), key, true)
	r, err := parseLoginRequest(legacy)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	r.Token = selfSignedToken(t, key, testIdentity())
	_, _, res, err := ParseWithConfig(encodeRequest(r), Config{})
	if err != nil {
		t.Fatalf("ParseWithConfig: %v", err)
	}
	if res.Method != MethodToken || !res.SentLegacyChain {
		t.Fatalf("result = %+v, want token with SentLegacyChain", res)
	}
}

func TestBadLengthsReturnErrors(t *testing.T) {
	for _, in := range [][]byte{nil, {1, 0, 0, 0}, {0xff, 0xff, 0xff, 0x7f, '{'}, {2, 0, 0, 0, '{', '}', 0xff, 0xff, 0xff, 0xff}} {
		if _, _, _, err := ParseWithConfig(in, Config{AllowLegacyChain: true}); err == nil {
			t.Fatalf("ParseWithConfig(%x) returned nil error", in)
		}
	}
}

// FuzzParse feeds random login requests to the parser. It must never panic.
func FuzzParse(f *testing.F) {
	key := testKey(f)
	tok, err := EncodeToken(selfSignedToken(f, key, testIdentity()), testClientData(), key)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(tok)
	f.Add(EncodeOffline(testIdentity(), testClientData(), key, true))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _, _, _ = ParseWithConfig(data, Config{AllowLegacyChain: true})
	})
}
