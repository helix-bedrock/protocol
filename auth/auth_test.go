package auth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // x5t thumbprints are SHA-1 by definition
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/helix-bedrock/protocol/auth"
	"github.com/helix-bedrock/protocol/login"
)

// fakeService stands in for discovery, the OpenID configuration and the
// key set of Minecraft's authorization service.
type fakeService struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	keys   map[string]*ecdsa.PrivateKey
	issuer string

	jwksFetches atomic.Int32
}

func newFakeService(t *testing.T) *fakeService {
	f := &fakeService{t: t, keys: map[string]*ecdsa.PrivateKey{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1.0/discovery/MinecraftPE/builds/", func(w http.ResponseWriter, _ *http.Request) {
		// Discovery names the issuer without a trailing slash; the
		// configuration and tokens use one.
		f.json(w, map[string]any{"result": map[string]any{"serviceEnvironments": map[string]any{
			"auth": map[string]any{"prod": map[string]any{"serviceUri": f.srv.URL, "issuer": f.srv.URL + "/issuer"}},
		}}})
	})
	mux.HandleFunc("/issuer/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		f.json(w, map[string]any{
			"issuer": f.issuer, "jwks_uri": f.srv.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"ES384"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		f.jwksFetches.Add(1)
		f.mu.Lock()
		defer f.mu.Unlock()
		var keys []map[string]any
		for kid, k := range f.keys {
			b, err := (&jose.JSONWebKey{Key: &k.PublicKey, KeyID: kid, Algorithm: "ES384", Use: "sig"}).MarshalJSON()
			if err != nil {
				t.Error(err)
				return
			}
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			// Microsoft writes the thumbprint in hex.
			sum := sha1.Sum([]byte(kid)) //nolint:gosec // test thumbprint
			m["x5t"] = hex.EncodeToString(sum[:])
			keys = append(keys, m)
		}
		f.json(w, map[string]any{"keys": keys})
	})
	f.srv = httptest.NewServer(mux)
	f.issuer = f.srv.URL + "/issuer/"
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeService) json(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		f.t.Error(err)
	}
}

func (f *fakeService) addKey(kid string) *ecdsa.PrivateKey {
	k := newKey(f.t)
	f.mu.Lock()
	f.keys[kid] = k
	f.mu.Unlock()
	return k
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

type claims struct {
	jwt.Claims
	CPK   string `json:"cpk"`
	XUID  string `json:"xid"`
	XName string `json:"xname"`
	MID   string `json:"mid"`
	TID   string `json:"tid"`
	IPT   string `json:"ipt"`
}

// token returns a multiplayer token signed by signer under kid, for the
// player whose key is client.
func token(t *testing.T, signer *ecdsa.PrivateKey, kid string, client *ecdsa.PrivateKey, edit func(*claims)) string {
	t.Helper()
	s, err := jose.NewSigner(jose.SigningKey{Key: signer, Algorithm: jose.ES384}, (&jose.SignerOptions{}).WithHeader("kid", kid))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	c := claims{
		Claims: jwt.Claims{
			Audience: jwt.Audience{auth.Audience}, Expiry: jwt.NewNumericDate(now.Add(time.Hour)),
			NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)), IssuedAt: jwt.NewNumericDate(now),
		},
		CPK: login.MarshalPublicKey(&client.PublicKey), XUID: "2535412345678901", XName: "Steve",
		MID: "ABCDEF", TID: "20CA2", IPT: "PlayFab",
	}
	if edit != nil {
		edit(&c)
	}
	out, err := jwt.Signed(s).Claims(c).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func clientData() login.ClientData {
	return login.ClientData{DeviceOS: 7, GameVersion: "1.26.50", LanguageCode: "en_US", ServerAddress: "127.0.0.1:19132", SkinID: "steve",
		SkinResourcePatch: base64.StdEncoding.EncodeToString([]byte(`{"geometry":{"default":"geometry.humanoid.custom"}}`))}
}

func parse(t *testing.T, v *auth.Verifier, tok string, client *ecdsa.PrivateKey) (login.IdentityData, login.AuthResult, error) {
	t.Helper()
	req, err := login.EncodeToken(tok, clientData(), client)
	if err != nil {
		t.Fatal(err)
	}
	ver, err := v.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	id, _, res, err := login.ParseWithConfig(req, login.Config{Verifier: ver})
	return id, res, err
}

func TestVerifiedLogin(t *testing.T) {
	f := newFakeService(t)
	svc := f.addKey("k1")
	v := auth.New(auth.Config{DiscoveryURL: f.srv.URL})
	client := newKey(t)

	id, res, err := parse(t, v, token(t, svc, "k1", client, func(c *claims) { c.Issuer = f.issuer }), client)
	if err != nil {
		t.Fatal(err)
	}
	if !res.XBOXLiveAuthenticated || id.XUID != "2535412345678901" || id.DisplayName != "Steve" {
		t.Fatalf("identity %+v, result %+v", id, res)
	}
	if !res.PublicKey.Equal(&client.PublicKey) {
		t.Fatal("encryption key is not the token's cpk")
	}
}

func TestRejectedTokens(t *testing.T) {
	f := newFakeService(t)
	svc := f.addKey("k1")
	v := auth.New(auth.Config{DiscoveryURL: f.srv.URL})
	client := newKey(t)
	iss := func(c *claims) { c.Issuer = f.issuer }

	for name, tok := range map[string]string{
		"wrong issuer": token(t, svc, "k1", client, func(c *claims) { c.Issuer = "https://evil.example/" }),
		"wrong audience": token(t, svc, "k1", client, func(c *claims) {
			iss(c)
			c.Audience = jwt.Audience{"api://something-else"}
		}),
		"expired": token(t, svc, "k1", client, func(c *claims) {
			iss(c)
			c.Expiry = jwt.NewNumericDate(time.Now().Add(-time.Hour))
		}),
		"not yet valid": token(t, svc, "k1", client, func(c *claims) {
			iss(c)
			c.NotBefore = jwt.NewNumericDate(time.Now().Add(time.Hour))
		}),
		"signed by an unknown key": token(t, newKey(t), "k1", client, iss),
		"self-signed":              token(t, client, "k1", client, iss),
		"bad xuid": token(t, svc, "k1", client, func(c *claims) {
			iss(c)
			c.XUID = "not-a-number"
		}),
	} {
		_, _, err := parse(t, v, tok, client)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		t.Logf("%s: %v", name, err)
	}

	// Client data signed by a key other than the token's cpk.
	other := newKey(t)
	if _, _, err := parse(t, v, token(t, svc, "k1", client, iss), other); err == nil {
		t.Error("client data signed by another key: accepted")
	}
}

func TestKeyRotation(t *testing.T) {
	f := newFakeService(t)
	old := f.addKey("old")
	v := auth.New(auth.Config{DiscoveryURL: f.srv.URL, KeyRefreshInterval: time.Nanosecond})
	client := newKey(t)
	iss := func(c *claims) { c.Issuer = f.issuer }

	if _, _, err := parse(t, v, token(t, old, "old", client, iss), client); err != nil {
		t.Fatal(err)
	}
	fetches := f.jwksFetches.Load()
	// Same key again: no new fetch.
	if _, _, err := parse(t, v, token(t, old, "old", client, iss), client); err != nil {
		t.Fatal(err)
	}
	if f.jwksFetches.Load() != fetches {
		t.Fatal("key set fetched again for a known key")
	}
	// The service starts signing with a new key.
	rotated := f.addKey("new")
	if _, _, err := parse(t, v, token(t, rotated, "new", client, iss), client); err != nil {
		t.Fatalf("token from rotated key: %v", err)
	}
}

func TestSetupFailureIsRetried(t *testing.T) {
	var up atomic.Bool
	f := newFakeService(t)
	f.addKey("k1")
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		http.Redirect(w, r, f.srv.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer proxy.Close()

	v := auth.New(auth.Config{DiscoveryURL: proxy.URL, RetryInterval: time.Millisecond})
	if _, err := v.Get(context.Background()); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("got %v, want 503 error", err)
	}
	up.Store(true)
	time.Sleep(2 * time.Millisecond)
	if _, err := v.Get(context.Background()); err != nil {
		t.Fatalf("after the service came back: %v", err)
	}
}
