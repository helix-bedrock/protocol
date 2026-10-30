package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/helix-bedrock/protocol/packet"
)

// fakeServices serves discovery, the OpenID configuration and the key set,
// all with a Date header from its own clock.
type fakeServices struct {
	srv       *httptest.Server
	key       *rsa.PrivateKey
	kid       string
	clock     time.Time
	keyFetchs atomic.Int32
}

func newFakeServices(t *testing.T) *fakeServices {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServices{key: key, kid: "K1", clock: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)}
	mux := http.NewServeMux()
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	date := func(w http.ResponseWriter) { w.Header().Set("Date", f.clock.Format(http.TimeFormat)) }
	mux.HandleFunc("/api/v1.0/discovery/MinecraftDedicatedServer/builds/1.26.60", func(w http.ResponseWriter, _ *http.Request) {
		date(w)
		_, _ = w.Write([]byte(`{"result":{"serviceEnvironments":{"auth":{"prod":{"issuer":"` + f.srv.URL + `"}}}}}`))
	})
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		date(w)
		_, _ = w.Write([]byte(`{"issuer":"` + f.srv.URL + `/","jwks_uri":"` + f.srv.URL + `/keys"}`))
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		f.keyFetchs.Add(1)
		date(w)
		n := base64.RawURLEncoding.EncodeToString(f.key.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "use": "sig", "kid": f.kid, "n": n, "e": e}}})
	})
	return f
}

// sign makes a token with the given claims, signed by key under kid.
func sign(t *testing.T, key *rsa.PrivateKey, kid string, c map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"})
	p, _ := json.Marshal(c)
	signed := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// started returns a Service whose local clock runs offset from the fake
// services' clock.
func started(t *testing.T, f *fakeServices, offset time.Duration) *Service {
	t.Helper()
	s := New(Config{DiscoveryURL: f.srv.URL, Build: "1.26.60", Clock: func() time.Time { return f.clock.Add(offset) }})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *fakeServices) claims(exp time.Time) map[string]any {
	return map[string]any{"aud": Audience, "iss": f.srv.URL + "/", "exp": exp.Unix(), "xid": "2535400000000000"}
}

func TestVerifyValidToken(t *testing.T) {
	f := newFakeServices(t)
	s := started(t, f, 0)
	payload, err := s.Verify(sign(t, f.key, f.kid, f.claims(f.clock.Add(time.Hour))))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		XID string `json:"xid"`
	}
	if json.Unmarshal(payload, &c) != nil || c.XID != "2535400000000000" {
		t.Fatalf("payload %s", payload)
	}
}

// TestClockCorrection checks that a server whose clock is two hours off
// still judges expiry by Microsoft's clock.
func TestClockCorrection(t *testing.T) {
	f := newFakeServices(t)
	for _, off := range []time.Duration{2 * time.Hour, -2 * time.Hour} {
		s := started(t, f, off)
		if got := s.Offset(); got != -off {
			t.Fatalf("local clock %v off: offset %v, want %v", off, got, -off)
		}
		if _, err := s.Verify(sign(t, f.key, f.kid, f.claims(f.clock.Add(time.Minute)))); err != nil {
			t.Fatalf("local clock %v off: valid token refused: %v", off, err)
		}
		if _, err := s.Verify(sign(t, f.key, f.kid, f.claims(f.clock.Add(-time.Minute)))); !errors.Is(err, ErrExpired) {
			t.Fatalf("local clock %v off: expired token: %v", off, err)
		}
	}
}

func TestVerifyFailures(t *testing.T) {
	f := newFakeServices(t)
	s := started(t, f, 0)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	valid := f.claims(f.clock.Add(time.Hour))
	with := func(k string, v any) map[string]any {
		c := map[string]any{}
		for key, val := range valid {
			c[key] = val
		}
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
		return c
	}
	otherEnv := with("aud", "x")
	otherEnv["iss"] = "https://other/"
	for _, tc := range []struct {
		name   string
		token  string
		want   error
		reason int32
	}{
		{"no exp", sign(t, f.key, f.kid, with("exp", nil)), ErrExpired, packet.DisconnectReasonExpiredToken},
		{"future nbf", sign(t, f.key, f.kid, with("nbf", f.clock.Add(time.Hour).Unix())), ErrIssueTime, packet.DisconnectReasonNotAuthenticated},
		{"future iat", sign(t, f.key, f.kid, with("iat", f.clock.Add(time.Hour).Unix())), ErrIssueTime, packet.DisconnectReasonNotAuthenticated},
		{"nbf within leeway", sign(t, f.key, f.kid, with("nbf", f.clock.Add(30*time.Second).Unix())), nil, 0},
		{"past nbf and iat", sign(t, f.key, f.kid, with("nbf", f.clock.Add(-time.Minute).Unix())), nil, 0},
		{"aud as array", sign(t, f.key, f.kid, with("aud", []string{Audience})), ErrNotAuthenticated, packet.DisconnectReasonNotAuthenticated},
		{"other environment", sign(t, f.key, f.kid, otherEnv), ErrEnvironment, packet.DisconnectReasonDiscoveryEnvironmentMismatch},
		{"unknown key", sign(t, f.key, "K2", valid), ErrNotAuthenticated, packet.DisconnectReasonNotAuthenticated},
		{"bad signature", sign(t, other, f.kid, valid), ErrNotAuthenticated, packet.DisconnectReasonNotAuthenticated},
		{"garbage", "a.b", ErrNotAuthenticated, packet.DisconnectReasonNotAuthenticated},
	} {
		_, err := s.Verify(tc.token)
		if !errors.Is(err, tc.want) && (tc.want != nil || err != nil) {
			t.Errorf("%s: err %v, want %v", tc.name, err, tc.want)
			continue
		}
		if err != nil && Reason(err) != tc.reason {
			t.Errorf("%s: reason %d, want %d", tc.name, Reason(err), tc.reason)
		}
	}
}

func TestNoKeys(t *testing.T) {
	s := New(Config{DiscoveryURL: "http://127.0.0.1:1", RetryInterval: time.Hour})
	f := newFakeServices(t)
	_, err := s.Verify(sign(t, f.key, f.kid, f.claims(time.Now().Add(time.Hour))))
	if !errors.Is(err, ErrNoKeys) || Reason(err) != packet.DisconnectReasonHostWithoutKeys {
		t.Fatalf("err %v", err)
	}
}

func TestUnknownKeyRefreshesOnce(t *testing.T) {
	f := newFakeServices(t)
	s := started(t, f, 0)
	before := f.keyFetchs.Load()
	f.kid = "K2" // the service rotated its key
	tok := sign(t, f.key, "K2", f.claims(f.clock.Add(time.Hour)))
	if _, err := s.Verify(tok); !errors.Is(err, ErrNotAuthenticated) {
		t.Fatalf("first try with a rotated key: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := s.Verify(tok); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rotated key never accepted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := f.keyFetchs.Load() - before; n != 1 {
		t.Fatalf("%d key downloads, want 1", n)
	}
}

func TestIgnoreIssueTimes(t *testing.T) {
	f := newFakeServices(t)
	s := New(Config{DiscoveryURL: f.srv.URL, Build: "1.26.60", IgnoreIssueTimes: true, Clock: func() time.Time { return f.clock }})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := f.claims(f.clock.Add(time.Hour))
	c["nbf"], c["iat"] = f.clock.Add(time.Hour).Unix(), f.clock.Add(time.Hour).Unix()
	if _, err := s.Verify(sign(t, f.key, f.kid, c)); err != nil {
		t.Fatalf("future nbf/iat refused with IgnoreIssueTimes: %v", err)
	}
}
