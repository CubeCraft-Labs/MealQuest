// Package secretbox encrypts secrets at rest with AES-256-GCM. It is the only
// owner of secret encryption: every _ct column holds a token produced by
// Box.Encrypt, laid out as nonce ‖ ciphertext.
//
// Errors from this package never contain plaintext, ciphertext or key
// material.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// KeySize is the decoded key length in bytes (AES-256).
const KeySize = 32

var (
	// ErrKeyEncoding means the key is not valid standard base64.
	ErrKeyEncoding = errors.New("secretbox: key is not valid standard base64")
	// ErrKeyLength means the decoded key is not exactly KeySize bytes.
	ErrKeyLength = errors.New("secretbox: key must decode to exactly 32 bytes")
	// ErrMalformed means a token is too short to hold a nonce and tag.
	ErrMalformed = errors.New("secretbox: token is too short")
	// ErrDecrypt means a token failed authentication: it was corrupted or
	// sealed under a different key.
	ErrDecrypt = errors.New("secretbox: token failed authentication")
)

// Box seals and opens secrets under one key. It is safe for concurrent use.
type Box struct {
	aead cipher.AEAD
}

// New returns a Box for encodedKey, the standard-base64 encoding of a
// KeySize-byte key.
func New(encodedKey string) (*Box, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
	if err != nil {
		// The decoder's error is not wrapped: it describes the key's content.
		return nil, ErrKeyEncoding
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w, got %d", ErrKeyLength, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secretbox: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secretbox: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Encrypt seals plaintext under a fresh random nonce and returns
// nonce ‖ ciphertext.
func (b *Box) Encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize(), b.aead.NonceSize()+len(plaintext)+b.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secretbox: generate nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt authenticates and opens a token produced by Encrypt.
func (b *Box) Decrypt(token []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(token) < n+b.aead.Overhead() {
		return nil, ErrMalformed
	}
	plaintext, err := b.aead.Open(nil, token[:n], token[n:], nil)
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}
