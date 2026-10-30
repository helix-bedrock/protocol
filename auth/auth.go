// Package auth verifies the multiplayer token a Bedrock client sends in its
// Login packet, so a server knows the player really signed in to Xbox Live.
//
// It works the way the Bedrock Dedicated Server 1.26.60 does:
//
//  1. At startup, Minecraft's service discovery is asked for the "auth"
//     service as a dedicated server (client type MinecraftDedicatedServer).
//  2. The auth service's OpenID configuration names the signing key set
//     (JWKS). Its HTTP Date header sets the server's corrected clock, so a
//     machine whose clock is wrong still accepts valid tokens.
//  3. The key set is downloaded. A token signed with a key the server does
//     not know is refused, and the key set is downloaded again in the
//     background (keys rotate).
//
// Verifying a token never makes a request, so it is cheap enough to run on
// every login and does not stall while Microsoft's services are slow.
package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/helix-bedrock/protocol"
)

// Audience is the audience ("aud") every multiplayer token must carry.
const Audience = "api://auth-minecraft-services/multiplayer"

// DefaultDiscoveryURL is Minecraft's service discovery endpoint.
const DefaultDiscoveryURL = "https://client.discovery.minecraft-services.net"

// ClientType is the client type a dedicated server names in discovery.
const ClientType = "MinecraftDedicatedServer"

// userAgent is sent with every request, as the game does.
const userAgent = "libhttpclient/1.0.0.0"

// maxBody bounds every response read from the services.
const maxBody = 1 << 20

// Config configures a [Service]. The zero value is valid.
type Config struct {
	// HTTPClient makes the requests. If nil, a client with a 15 second
	// timeout.
	HTTPClient *http.Client
	// DiscoveryURL is the discovery endpoint. If empty,
	// DefaultDiscoveryURL. Tests point it at a local server.
	DiscoveryURL string
	// Build is the game version asked for in discovery. If empty,
	// protocol.CurrentVersion.
	Build string
	// Environment is the discovery environment used. If empty, "prod".
	Environment string
	// RefreshInterval is the shortest time between two key set downloads
	// caused by tokens with unknown keys. If 0, one minute. BDS's own limit
	// is not known; this keeps a flood of bad tokens from turning into a
	// flood of requests.
	RefreshInterval time.Duration
	// RetryInterval is the shortest time between two attempts to set up
	// after a failed one. If 0, 30 seconds.
	RetryInterval time.Duration
	// IgnoreIssueTimes turns off the "nbf" and "iat" checks, which BDS does
	// not do. Helix does them by default against the corrected clock.
	IgnoreIssueTimes bool
	// IssueTimeLeeway is how far "nbf" and "iat" may be ahead of the
	// corrected clock, for timing differences between Microsoft's servers.
	// If 0, one minute.
	IssueTimeLeeway time.Duration
	// Clock returns the local time. If nil, time.Now. Tests set it.
	Clock func() time.Time
}

// Service verifies multiplayer tokens against Minecraft's auth service. One
// Service serves the whole server; it is safe for use by many goroutines at
// once.
type Service struct {
	conf Config

	// state is replaced as a whole, so Verify reads it without a lock.
	state atomic.Pointer[state]
	// offset is the corrected clock minus the local clock, in nanoseconds.
	offset atomic.Int64

	mu         sync.Mutex
	setupTry   time.Time
	setupErr   error
	refreshAt  time.Time
	refreshing bool
}

// state is what setup learned: the issuer and its keys by key ID.
type state struct {
	issuer  string
	jwksURL string
	keys    map[string]*rsa.PublicKey
}

// New returns a Service. It makes no requests until [Service.Start] or the
// first [Service.Verify].
func New(conf Config) *Service {
	if conf.HTTPClient == nil {
		conf.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if conf.DiscoveryURL == "" {
		conf.DiscoveryURL = DefaultDiscoveryURL
	}
	if conf.Build == "" {
		conf.Build = protocol.CurrentVersion
	}
	if conf.Environment == "" {
		conf.Environment = "prod"
	}
	if conf.RefreshInterval <= 0 {
		conf.RefreshInterval = time.Minute
	}
	if conf.RetryInterval <= 0 {
		conf.RetryInterval = 30 * time.Second
	}
	if conf.IssueTimeLeeway <= 0 {
		conf.IssueTimeLeeway = time.Minute
	}
	if conf.Clock == nil {
		conf.Clock = time.Now
	}
	return &Service{conf: conf}
}

// Start sets the service up now: discovery, OpenID configuration and keys.
// BDS does this when it starts. A server may start anyway when it fails:
// logins are then refused with HostWithoutKeys, and setup is tried again in
// the background at most once per RetryInterval.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	s.setupTry = s.conf.Clock()
	s.mu.Unlock()
	err := s.setup(ctx)
	s.mu.Lock()
	s.setupErr = err
	s.mu.Unlock()
	return err
}

// Ready reports whether the service has keys to verify tokens with.
func (s *Service) Ready() bool {
	st := s.state.Load()
	return st != nil && len(st.keys) > 0
}

// Issuer returns the issuer learned from the auth service, or "" before
// setup.
func (s *Service) Issuer() string {
	if st := s.state.Load(); st != nil {
		return st.issuer
	}
	return ""
}

// Now returns the corrected clock: the local time moved by the difference
// to Microsoft's clock seen in the last response's Date header.
func (s *Service) Now() time.Time {
	return s.conf.Clock().Add(time.Duration(s.offset.Load()))
}

// Offset returns how far the corrected clock is ahead of the local one.
func (s *Service) Offset() time.Duration { return time.Duration(s.offset.Load()) }

func (s *Service) setup(ctx context.Context) error {
	issuer, err := s.discover(ctx)
	if err != nil {
		return fmt.Errorf("auth: discovery: %w", err)
	}
	cfg, err := s.openIDConfig(ctx, issuer)
	if err != nil {
		return fmt.Errorf("auth: openid configuration: %w", err)
	}
	keys, err := s.downloadKeys(ctx, cfg.JWKSURL)
	if err != nil {
		return fmt.Errorf("auth: keys: %w", err)
	}
	s.state.Store(&state{issuer: cfg.Issuer, jwksURL: cfg.JWKSURL, keys: keys})
	return nil
}

// trySetup starts a background setup when there is no state yet and the
// last attempt is older than RetryInterval.
func (s *Service) trySetup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refreshing || s.conf.Clock().Sub(s.setupTry) < s.conf.RetryInterval {
		return
	}
	s.refreshing, s.setupTry = true, s.conf.Clock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		err := s.setup(ctx)
		cancel()
		s.mu.Lock()
		s.refreshing, s.setupErr = false, err
		s.mu.Unlock()
	}()
}

// refreshKeys downloads the key set again in the background, at most once
// per RefreshInterval, as BDS does when a token names a key it lacks.
func (s *Service) refreshKeys() {
	st := s.state.Load()
	if st == nil {
		s.trySetup()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.conf.Clock()
	if s.refreshing || now.Before(s.refreshAt) {
		return
	}
	s.refreshing, s.refreshAt = true, now.Add(s.conf.RefreshInterval)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		keys, err := s.downloadKeys(ctx, st.jwksURL)
		cancel()
		if err == nil {
			s.state.Store(&state{issuer: st.issuer, jwksURL: st.jwksURL, keys: keys})
		}
		s.mu.Lock()
		s.refreshing = false
		s.mu.Unlock()
	}()
}

// discover returns the issuer of the auth service from discovery.
func (s *Service) discover(ctx context.Context) (string, error) {
	u, err := url.Parse(s.conf.DiscoveryURL)
	if err != nil {
		return "", err
	}
	u = u.JoinPath("/api/v1.0/discovery", ClientType, "builds", s.conf.Build)
	var res struct {
		Result struct {
			ServiceEnvironments map[string]map[string]struct {
				Issuer string `json:"issuer"`
			} `json:"serviceEnvironments"`
		} `json:"result"`
	}
	if err := s.getJSON(ctx, u.String(), &res); err != nil {
		return "", err
	}
	env, ok := res.Result.ServiceEnvironments["auth"][s.conf.Environment]
	if !ok {
		return "", fmt.Errorf("no auth service in environment %q", s.conf.Environment)
	}
	if env.Issuer == "" {
		return "", errors.New("auth service has no issuer")
	}
	return env.Issuer, nil
}

// openIDConfiguration is the part of the OpenID configuration BDS reads.
type openIDConfiguration struct {
	Issuer  string `json:"issuer"`
	JWKSURL string `json:"jwks_uri"`
}

func (s *Service) openIDConfig(ctx context.Context, issuer string) (openIDConfiguration, error) {
	var cfg openIDConfiguration
	u, err := url.Parse(issuer)
	if err != nil {
		return cfg, fmt.Errorf("parse issuer: %w", err)
	}
	if err := s.getJSON(ctx, u.JoinPath("/.well-known/openid-configuration").String(), &cfg); err != nil {
		return cfg, err
	}
	if cfg.Issuer == "" || cfg.JWKSURL == "" {
		return cfg, errors.New("missing issuer or jwks_uri")
	}
	return cfg, nil
}

// jwk is one RSA key of the key set.
type jwk struct {
	KeyType string `json:"kty"`
	Use     string `json:"use"`
	KeyID   string `json:"kid"`
	N       string `json:"n"`
	E       string `json:"e"`
}

// downloadKeys returns the RSA signing keys of the key set by key ID. Keys of
// other types or uses are skipped: the service signs with RS256 only.
func (s *Service) downloadKeys(ctx context.Context, u string) (map[string]*rsa.PublicKey, error) {
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := s.getJSON(ctx, u, &set); err != nil {
		return nil, err
	}
	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.KeyType != "RSA" || (k.Use != "" && k.Use != "sig") || k.KeyID == "" {
			continue
		}
		pub, err := rsaKey(k.N, k.E)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k.KeyID, err)
		}
		keys[k.KeyID] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("key set has no RSA signing keys")
	}
	return keys, nil
}

// rsaKey decodes a JWK's base64url modulus and exponent.
func rsaKey(n, e string) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(n)
	if err != nil {
		return nil, fmt.Errorf("modulus: %w", err)
	}
	eb, err := base64.RawURLEncoding.DecodeString(e)
	if err != nil {
		return nil, fmt.Errorf("exponent: %w", err)
	}
	if len(nb) < 256 || len(eb) == 0 || len(eb) > 4 {
		return nil, fmt.Errorf("modulus of %d bytes or exponent of %d bytes", len(nb), len(eb))
	}
	var exp int
	for _, b := range eb {
		exp = exp<<8 | int(b)
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: exp}, nil
}

// getJSON fetches u into dst and moves the corrected clock to the response's
// Date header.
func (s *Service) getJSON(ctx context.Context, u string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.conf.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	s.correctClock(resp.Header.Get("Date"))
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(dst); err != nil {
		return fmt.Errorf("decode %s: %w", u, err)
	}
	return nil
}

// correctClock sets the clock offset from an HTTP Date header. A missing or
// malformed header leaves the offset as it was.
func (s *Service) correctClock(date string) {
	if date == "" {
		return
	}
	t, err := http.ParseTime(date)
	if err != nil {
		return
	}
	s.offset.Store(int64(t.Sub(s.conf.Clock())))
}
