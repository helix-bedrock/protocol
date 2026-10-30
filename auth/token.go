package auth

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/helix-bedrock/protocol/packet"
)

// Errors returned by [Service.Verify]. [Reason] gives the disconnect reason
// BDS sends for each.
var (
	// ErrExpired: the token's "exp" has passed on the corrected clock.
	ErrExpired = errors.New("auth: token expired")
	// ErrNoKeys: the service has no keys yet (setup failed or is running).
	ErrNoKeys = errors.New("auth: no keys to verify tokens with")
	// ErrEnvironment: the token comes from another auth environment than
	// the one discovery named.
	ErrEnvironment = errors.New("auth: token from another environment")
	// ErrIssueTime: "nbf" or "iat" is in the future on the corrected clock.
	// BDS does not check these; Helix does unless Config.IgnoreIssueTimes.
	ErrIssueTime = errors.New("auth: token not valid yet")
	// ErrNotAuthenticated: anything else, such as a bad audience, an
	// unknown key or a bad signature.
	ErrNotAuthenticated = errors.New("auth: token not authenticated")
)

// Reason returns the disconnect reason BDS 1.26.60 sends for an error from
// [Service.Verify].
func Reason(err error) int32 {
	switch {
	case errors.Is(err, ErrExpired):
		return packet.DisconnectReasonExpiredToken
	case errors.Is(err, ErrNoKeys):
		return packet.DisconnectReasonHostWithoutKeys
	case errors.Is(err, ErrEnvironment):
		return packet.DisconnectReasonDiscoveryEnvironmentMismatch
	default:
		return packet.DisconnectReasonNotAuthenticated
	}
}

// claims are the token claims checked here. Identity claims are read by the
// caller from the payload Verify returns.
type claims struct {
	Exp json.Number     `json:"exp"`
	Aud json.RawMessage `json:"aud"`
	Iss string          `json:"iss"`
	Nbf json.Number     `json:"nbf"`
	Iat json.Number     `json:"iat"`
}

// header is the token's JOSE header.
type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

// Verify checks a multiplayer token and returns its JSON payload. It follows
// BDS 1.26.60 in order and in what it checks:
//
//  1. "exp" must not have passed on the corrected clock (ErrExpired). A
//     token without "exp" is expired.
//  2. "aud" must be exactly [Audience], as a string.
//  3. The key named by "kid" must be known and the RS256 signature valid.
//
// Then, unless Config.IgnoreIssueTimes is set, "nbf" and "iat" (when present)
// must not be later than the corrected clock plus Config.IssueTimeLeeway
// (ErrIssueTime). BDS does not check them; this runs only after the
// signature passed, so every BDS case still gets BDS's reason.
//
// When check 2 or 3 fails,
// ErrNoKeys is returned while the service has no keys; an unknown key or bad
// signature also starts a background key download (keys rotate); a failed
// audience is ErrEnvironment when the token's issuer is not the service's.
//
// Verify makes no requests and is safe for use by many goroutines at once.
func (s *Service) Verify(token string) ([]byte, error) {
	h, payload, signed, sig, ok := split(token)
	if !ok {
		return nil, ErrNotAuthenticated
	}
	var c claims
	if json.Unmarshal(payload, &c) != nil {
		return nil, ErrNotAuthenticated
	}
	exp, err := c.Exp.Int64()
	if err != nil || s.Now().Unix() > exp {
		return nil, ErrExpired
	}
	audOK := audience(c.Aud) == Audience
	st := s.state.Load()
	if st == nil || len(st.keys) == 0 {
		s.trySetup()
		return nil, ErrNoKeys
	}
	if !audOK {
		if c.Iss != st.issuer {
			return nil, ErrEnvironment
		}
		return nil, ErrNotAuthenticated
	}
	key, known := st.keys[h.Kid]
	if !known || h.Alg != "RS256" || !verifyRS256(key, signed, sig) {
		s.refreshKeys()
		return nil, ErrNotAuthenticated
	}
	if !s.conf.IgnoreIssueTimes {
		limit := s.Now().Add(s.conf.IssueTimeLeeway).Unix()
		if future(c.Nbf, limit) || future(c.Iat, limit) {
			return nil, ErrIssueTime
		}
	}
	return payload, nil
}

// future reports whether a present time claim is later than limit. A
// claim that is not a number counts as in the future.
func future(n json.Number, limit int64) bool {
	if n == "" {
		return false
	}
	v, err := n.Int64()
	return err != nil || v > limit
}

// audience reads "aud" as a string. BDS reads it as a string too, so an
// array, as some OpenID issuers write, does not match.
func audience(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// split decodes a compact JWS into its header, payload, signed part and
// signature.
func split(token string) (h header, payload []byte, signed string, sig []byte, ok bool) {
	first := strings.IndexByte(token, '.')
	last := strings.LastIndexByte(token, '.')
	if first <= 0 || last <= first || last == len(token)-1 {
		return h, nil, "", nil, false
	}
	hb, err := base64.RawURLEncoding.DecodeString(token[:first])
	if err != nil || json.Unmarshal(hb, &h) != nil {
		return h, nil, "", nil, false
	}
	payload, err = base64.RawURLEncoding.DecodeString(token[first+1 : last])
	if err != nil {
		return h, nil, "", nil, false
	}
	sig, err = base64.RawURLEncoding.DecodeString(token[last+1:])
	if err != nil {
		return h, nil, "", nil, false
	}
	return h, payload, token[:last], sig, true
}

func verifyRS256(key *rsa.PublicKey, signed string, sig []byte) bool {
	sum := sha256.Sum256([]byte(signed))
	return rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) == nil
}
