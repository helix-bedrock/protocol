package auth

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// keySet is an oidc.KeySet backed by the authorization service's JWKS. It
// fetches the keys when it has none, and again when a token names a key ID it
// does not know, at most once per refresh interval. Concurrent refreshes are
// merged into one request.
//
// Ported from gophertunnel's refreshingKeySet (minecraft/service/jwks.go at
// v1.62.0, MIT).
type keySet struct {
	client   *http.Client
	url      string
	interval time.Duration
	algs     []jose.SignatureAlgorithm

	mu        sync.RWMutex
	keys      []jose.JSONWebKey
	lastFetch time.Time
	inflight  *fetch
}

type fetch struct {
	done chan struct{}
	keys []jose.JSONWebKey
	err  error
}

func newKeySet(client *http.Client, url string, interval time.Duration, algs []string) *keySet {
	ks := &keySet{client: client, url: url, interval: interval}
	for _, a := range algs {
		if a != "" {
			ks.algs = append(ks.algs, jose.SignatureAlgorithm(a))
		}
	}
	if len(ks.algs) == 0 {
		// RS256 is mandatory for OpenID Connect providers.
		ks.algs = []jose.SignatureAlgorithm{jose.RS256}
	}
	return ks
}

// VerifySignature implements oidc.KeySet.
func (ks *keySet) VerifySignature(ctx context.Context, token string) ([]byte, error) {
	jws, err := jose.ParseSigned(token, ks.algs)
	if err != nil {
		return nil, fmt.Errorf("auth: malformed token: %w", err)
	}
	kid := ""
	if len(jws.Signatures) > 0 {
		kid = jws.Signatures[0].Header.KeyID
	}

	ks.mu.RLock()
	keys, last := ks.keys, ks.lastFetch
	ks.mu.RUnlock()
	if payload, ok := verifyWith(jws, keys, kid); ok {
		return payload, nil
	}

	refresh := len(keys) == 0 ||
		(kid != "" && !hasKey(keys, kid) && time.Since(last) >= ks.interval)
	if refresh {
		keys, err := ks.fetchKeys(ctx)
		if err != nil {
			return nil, fmt.Errorf("auth: fetch keys: %w", err)
		}
		if payload, ok := verifyWith(jws, keys, kid); ok {
			return payload, nil
		}
	}
	return nil, errors.New("auth: token signature does not match any key of the authorization service")
}

func verifyWith(jws *jose.JSONWebSignature, keys []jose.JSONWebKey, kid string) ([]byte, bool) {
	for i := range keys {
		if kid == "" || keys[i].KeyID == kid {
			if payload, err := jws.Verify(&keys[i]); err == nil {
				return payload, true
			}
		}
	}
	return nil, false
}

func hasKey(keys []jose.JSONWebKey, kid string) bool {
	for _, k := range keys {
		if k.KeyID == kid {
			return true
		}
	}
	return false
}

// fetchKeys fetches the key set, sharing one request among concurrent
// callers.
func (ks *keySet) fetchKeys(ctx context.Context) ([]jose.JSONWebKey, error) {
	ks.mu.Lock()
	f := ks.inflight
	if f == nil {
		f = &fetch{done: make(chan struct{})}
		ks.inflight = f
		go func() {
			// Not tied to ctx: other callers may be waiting on it.
			keys, err := ks.download()
			f.keys, f.err = keys, err
			ks.mu.Lock()
			if err == nil {
				ks.keys, ks.lastFetch = keys, time.Now()
			}
			ks.inflight = nil
			ks.mu.Unlock()
			close(f.done)
		}()
	}
	ks.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.done:
		return f.keys, f.err
	}
}

func (ks *keySet) download() ([]jose.JSONWebKey, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ks.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := ks.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", ks.url, resp.Status)
	}
	return decodeKeySet(io.LimitReader(resp.Body, 1<<20))
}

// decodeKeySet decodes a JWKS. Microsoft writes the "x5t" thumbprint in hex
// instead of base64url as the JOSE spec says, which go-jose rejects, so it is
// converted first.
func decodeKeySet(r io.Reader) ([]jose.JSONWebKey, error) {
	var data struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.NewDecoder(r).Decode(&data); err != nil {
		return nil, err
	}
	keys := make([]jose.JSONWebKey, 0, len(data.Keys))
	for i, key := range data.Keys {
		if x5t, ok := key["x5t"].(string); ok {
			var fp []byte
			var err error
			switch len(x5t) {
			case 40: // hex SHA-1
				fp, err = hex.DecodeString(x5t)
				key["x5t"] = base64.RawURLEncoding.EncodeToString(fp)
			case 27: // base64url SHA-1
				fp, err = base64.RawURLEncoding.DecodeString(x5t)
			default:
				err = fmt.Errorf("x5t has length %d", len(x5t))
			}
			if err != nil {
				return nil, fmt.Errorf("key %d: x5t: %w", i, err)
			}
			if len(fp) != 20 {
				return nil, fmt.Errorf("key %d: x5t is %d bytes, want 20", i, len(fp))
			}
		}
		b, err := json.Marshal(key)
		if err != nil {
			return nil, fmt.Errorf("key %d: %w", i, err)
		}
		var jwk jose.JSONWebKey
		if err := jwk.UnmarshalJSON(b); err != nil {
			return nil, fmt.Errorf("key %d: %w", i, err)
		}
		keys = append(keys, jwk)
	}
	return keys, nil
}
