package login

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// SaltSize is the size of the salt in the server's handshake token.
const SaltSize = 16

// ServerHandshake makes the token of a ServerToClientHandshake packet and the
// key that encrypts the connection after it, as BDS 1.26.60 does:
//
//   - the token is an ES384 JWT signed by the server's key, whose public key
//     is in the "x5u" header, holding a "salt" claim of SaltSize random
//     bytes in padded standard base64;
//   - the key is SHA-256 of the salt followed by the ECDH secret of the
//     server's key and the client's public key (from the login).
//
// BDS signs every handshake with one key it holds for the whole server, so
// no key is generated per player. server must be a P-384 key.
func ServerHandshake(server *ecdsa.PrivateKey, client *ecdsa.PublicKey) (token []byte, key [32]byte, err error) {
	if server == nil || client == nil || server.Curve != elliptic.P384() || client.Curve != elliptic.P384() {
		return nil, key, errors.New("login: handshake needs P-384 keys")
	}
	var salt [SaltSize]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return nil, key, fmt.Errorf("login: handshake salt: %w", err)
	}
	priv, err := server.ECDH()
	if err != nil {
		return nil, key, fmt.Errorf("login: server key: %w", err)
	}
	pub, err := client.ECDH()
	if err != nil {
		return nil, key, fmt.Errorf("login: client key: %w", err)
	}
	secret, err := priv.ECDH(pub)
	if err != nil {
		return nil, key, fmt.Errorf("login: key exchange: %w", err)
	}
	key = sha256.Sum256(append(salt[:], secret...))

	header, err := json.Marshal(handshakeHeader{Alg: "ES384", X5U: MarshalPublicKey(&server.PublicKey)})
	if err != nil {
		return nil, key, err
	}
	claims, err := json.Marshal(handshakeClaims{Salt: base64.StdEncoding.EncodeToString(salt[:])})
	if err != nil {
		return nil, key, err
	}
	enc := base64.RawURLEncoding
	signed := make([]byte, 0, enc.EncodedLen(len(header))+enc.EncodedLen(len(claims))+130)
	signed = enc.AppendEncode(signed, header)
	signed = append(signed, '.')
	signed = enc.AppendEncode(signed, claims)
	sig, err := signES384(server, signed)
	if err != nil {
		return nil, key, fmt.Errorf("login: sign handshake: %w", err)
	}
	signed = append(signed, '.')
	return enc.AppendEncode(signed, sig), key, nil
}

type handshakeHeader struct {
	Alg string `json:"alg"`
	X5U string `json:"x5u"`
}

type handshakeClaims struct {
	Salt string `json:"salt"`
}

// signES384 returns the JWS signature of data: r and s, 48 bytes each.
func signES384(key *ecdsa.PrivateKey, data []byte) ([]byte, error) {
	sum := sha512.Sum384(data)
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return nil, err
	}
	sig := make([]byte, 96)
	r.FillBytes(sig[:48])
	s.FillBytes(sig[48:])
	return sig, nil
}
