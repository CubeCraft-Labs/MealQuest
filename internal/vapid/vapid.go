// Package vapid generates and validates the VAPID key pair Web Push signs
// with (RFC 8292): a P-256 (ES256) key pair in the encoding webpush-go
// reads. Both keys are unpadded base64url: the public key is the 65-byte
// uncompressed point and the private key the 32-byte scalar.
package vapid

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// encoding is unpadded base64url. Strict refuses non-zero trailing bits, so
// every key has exactly one accepted spelling.
var encoding = base64.RawURLEncoding.Strict()

const (
	publicKeySize  = 65 // 0x04 || X || Y
	privateKeySize = 32
)

// KeyPair is a VAPID key pair in its stored encoding.
type KeyPair struct {
	PublicKey  string
	PrivateKey string
}

// Generate returns a new random key pair.
func Generate() (KeyPair, error) {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, fmt.Errorf("vapid: generate key: %w", err)
	}
	return KeyPair{
		PublicKey:  encoding.EncodeToString(k.PublicKey().Bytes()),
		PrivateKey: encoding.EncodeToString(k.Bytes()),
	}, nil
}

// ParsePublicKey parses an encoded public key, which must be a point on
// P-256.
func ParsePublicKey(s string) (*ecdh.PublicKey, error) {
	b, err := encoding.DecodeString(s)
	if err != nil {
		return nil, errors.New("not unpadded base64url")
	}
	if len(b) != publicKeySize || b[0] != 0x04 {
		return nil, fmt.Errorf("want the %d-byte uncompressed P-256 point, got %d bytes", publicKeySize, len(b))
	}
	k, err := ecdh.P256().NewPublicKey(b)
	if err != nil {
		return nil, errors.New("not a point on P-256")
	}
	return k, nil
}

// ParsePrivateKey parses an encoded private key. Its errors never quote the
// key.
func ParsePrivateKey(s string) (*ecdh.PrivateKey, error) {
	b, err := encoding.DecodeString(s)
	if err != nil {
		return nil, errors.New("not unpadded base64url")
	}
	if len(b) != privateKeySize {
		return nil, fmt.Errorf("want a %d-byte P-256 scalar, got %d bytes", privateKeySize, len(b))
	}
	k, err := ecdh.P256().NewPrivateKey(b)
	if err != nil {
		return nil, errors.New("not a valid P-256 scalar")
	}
	return k, nil
}

// ErrMismatch is returned by CheckPair when the private key's public key is
// not the given public key.
var ErrMismatch = errors.New("private key does not belong to the public key")

// CheckPair reports whether private is the private key of public, by
// deriving the public point from private and comparing.
func CheckPair(public *ecdh.PublicKey, private *ecdh.PrivateKey) error {
	if !private.PublicKey().Equal(public) {
		return ErrMismatch
	}
	return nil
}
