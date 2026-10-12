// Package auth verifies the multiplayer token a Bedrock client sends in its
// Login packet, so a server knows the player really signed in to Xbox Live.
//
// The token is an OpenID Connect ID token issued by Minecraft's
// authorization service. Verifying it takes three lookups, all cached:
//
//  1. Minecraft's service discovery names the authorization service and its
//     issuer.
//  2. The issuer's OpenID configuration names the signing key set (JWKS) and
//     the signature algorithms.
//  3. The key set is fetched, and fetched again when a token is signed with
//     a key it does not know (keys rotate), at most once per refresh
//     interval.
//
// Ported from gophertunnel (minecraft/service and minecraft/listener.go at
// v1.62.0, MIT); see CREDITS.md. Pumpkin's pumpkin-auth was read to compare
// which claims it checks.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/helix-bedrock/protocol"
)

// Audience is the audience ("aud") every multiplayer token must carry.
const Audience = "api://auth-minecraft-services/multiplayer"

// DefaultDiscoveryURL is Minecraft's service discovery endpoint.
const DefaultDiscoveryURL = "https://client.discovery.minecraft-services.net"

// userAgent is sent with every request, as the game and gophertunnel do.
const userAgent = "libhttpclient/1.0.0.0"

// Config configures a [Verifier].
type Config struct {
	// HTTPClient makes the requests. Nil means a client with a 15 second
	// timeout.
	HTTPClient *http.Client
	// DiscoveryURL is the discovery endpoint. Empty means
	// DefaultDiscoveryURL. Tests point it at a local server.
	DiscoveryURL string
	// Version is the game version asked for in discovery. Empty means
	// protocol.CurrentVersion.
	Version string
	// KeyRefreshInterval is the shortest time between two key set fetches
	// caused by unknown key IDs. Zero means 30 minutes.
	KeyRefreshInterval time.Duration
	// RetryInterval is the shortest time between two attempts to set up
	// the verifier after one failed. Zero means 30 seconds.
	RetryInterval time.Duration
}

// Verifier builds and caches the OpenID verifier for multiplayer tokens. It
// sets itself up on first use and again after a failure, so a server that
// starts while Minecraft's services are down recovers on its own.
//
// It is safe for use by many goroutines at once.
type Verifier struct {
	conf Config

	mu       sync.Mutex
	verifier *oidc.IDTokenVerifier
	lastErr  error
	lastTry  time.Time
}

// New returns a Verifier. It makes no requests until [Verifier.Get].
func New(conf Config) *Verifier {
	if conf.HTTPClient == nil {
		conf.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if conf.DiscoveryURL == "" {
		conf.DiscoveryURL = DefaultDiscoveryURL
	}
	if conf.Version == "" {
		conf.Version = protocol.CurrentVersion
	}
	if conf.KeyRefreshInterval <= 0 {
		conf.KeyRefreshInterval = 30 * time.Minute
	}
	if conf.RetryInterval <= 0 {
		conf.RetryInterval = 30 * time.Second
	}
	return &Verifier{conf: conf}
}

// Get returns the verifier to pass to login.Config.Verifier, setting it up
// if needed. After a failure it returns the same error until
// RetryInterval has passed, so a burst of logins does not flood the
// services.
func (v *Verifier) Get(ctx context.Context) (*oidc.IDTokenVerifier, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.verifier != nil {
		return v.verifier, nil
	}
	if v.lastErr != nil && time.Since(v.lastTry) < v.conf.RetryInterval {
		return nil, v.lastErr
	}
	v.lastTry = time.Now()
	ver, err := v.setup(ctx)
	if err != nil {
		v.lastErr = fmt.Errorf("auth: set up token verifier: %w", err)
		return nil, v.lastErr
	}
	v.verifier, v.lastErr = ver, nil
	return ver, nil
}

func (v *Verifier) setup(ctx context.Context) (*oidc.IDTokenVerifier, error) {
	issuer, err := v.discover(ctx)
	if err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	cfg, err := v.openIDConfig(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("openid configuration: %w", err)
	}
	if cfg.JWKSURL == "" {
		return nil, errors.New("openid configuration has no jwks_uri")
	}
	keys := newKeySet(v.conf.HTTPClient, cfg.JWKSURL, v.conf.KeyRefreshInterval, cfg.Algorithms)
	// Tokens must name exactly the issuer of the OpenID configuration, as
	// the OpenID Connect spec requires. Discovery's issuer may differ by a
	// trailing slash (gophertunnel appends one), so it is only compared
	// loosely in openIDConfig.
	return oidc.NewVerifier(cfg.IssuerURL, keys, &oidc.Config{
		ClientID:             Audience,
		SupportedSigningAlgs: cfg.Algorithms,
	}), nil
}

// discover returns the issuer of the "auth" service from discovery.
func (v *Verifier) discover(ctx context.Context) (string, error) {
	u, err := url.Parse(v.conf.DiscoveryURL)
	if err != nil {
		return "", err
	}
	u = u.JoinPath("/api/v1.0/discovery/MinecraftPE/builds", v.conf.Version)
	var res struct {
		Result struct {
			ServiceEnvironments map[string]map[string]json.RawMessage `json:"serviceEnvironments"`
		} `json:"result"`
	}
	if err := v.getJSON(ctx, u.String(), &res); err != nil {
		return "", err
	}
	raw, ok := res.Result.ServiceEnvironments["auth"]["prod"]
	if !ok {
		return "", errors.New(`no "auth" "prod" environment`)
	}
	var env struct {
		Issuer string `json:"issuer"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("decode auth environment: %w", err)
	}
	if env.Issuer == "" {
		return "", errors.New("auth environment has no issuer")
	}
	return env.Issuer, nil
}

func (v *Verifier) openIDConfig(ctx context.Context, issuer string) (*oidc.ProviderConfig, error) {
	u, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("parse issuer: %w", err)
	}
	var cfg oidc.ProviderConfig
	if err := v.getJSON(ctx, u.JoinPath("/.well-known/openid-configuration").String(), &cfg); err != nil {
		return nil, err
	}
	if cfg.IssuerURL == "" {
		cfg.IssuerURL = issuer
	}
	if !sameIssuer(cfg.IssuerURL, issuer) {
		return nil, fmt.Errorf("issuer %q in configuration does not match discovery issuer %q", cfg.IssuerURL, issuer)
	}
	return &cfg, nil
}

// sameIssuer compares issuers ignoring one trailing slash.
func sameIssuer(a, b string) bool {
	return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}

func (v *Verifier) getJSON(ctx context.Context, u string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := v.conf.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("decode %s: %w", u, err)
	}
	return nil
}
