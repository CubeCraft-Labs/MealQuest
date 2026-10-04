package secretbox

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// newKey returns a fresh random key and its standard-base64 encoding.
func newKey(t *testing.T) ([]byte, string) {
	t.Helper()
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key, base64.StdEncoding.EncodeToString(key)
}

func newBox(t *testing.T) (*Box, []byte, string) {
	t.Helper()
	key, encoded := newKey(t)
	box, err := New(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return box, key, encoded
}

// assertNoLeak fails if err's message contains any of the secrets, either
// raw or base64-encoded.
func assertNoLeak(t *testing.T, err error, secrets ...[]byte) {
	t.Helper()
	msg := err.Error()
	for _, s := range secrets {
		if len(s) == 0 {
			continue
		}
		for _, form := range []string{string(s), base64.StdEncoding.EncodeToString(s), base64.RawStdEncoding.EncodeToString(s)} {
			if strings.Contains(msg, form) {
				t.Errorf("error %q leaks secret material %q", msg, form)
			}
		}
	}
}

func TestRoundTrip(t *testing.T) {
	box, _, _ := newBox(t)
	for _, plaintext := range [][]byte{
		[]byte("ya29.oauth-refresh-token"),
		{},
		bytes.Repeat([]byte{0x00, 0xff}, 4096),
	} {
		token, err := box.Encrypt(plaintext)
		if err != nil {
			t.Fatal(err)
		}
		if len(plaintext) > 0 && bytes.Contains(token, plaintext) {
			t.Errorf("token contains the plaintext")
		}
		got, err := box.Decrypt(token)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Errorf("Decrypt = %q, want %q", got, plaintext)
		}
	}
}

func TestTokenLayout(t *testing.T) {
	box, _, _ := newBox(t)
	plaintext := []byte("secret")
	token, err := box.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	// nonce (12) ‖ ciphertext (len(plaintext)) ‖ GCM tag (16).
	if want := 12 + len(plaintext) + 16; len(token) != want {
		t.Errorf("token length = %d, want %d", len(token), want)
	}
}

func TestEncryptUsesFreshNonce(t *testing.T) {
	box, _, _ := newBox(t)
	plaintext := []byte("same plaintext")
	a, err := box.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	b, err := box.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions of the same plaintext are identical")
	}
	if bytes.Equal(a[:12], b[:12]) {
		t.Error("two encryptions reused the same nonce")
	}
}

func TestDecryptRejectsFlippedByte(t *testing.T) {
	box, key, encoded := newBox(t)
	plaintext := []byte("do-not-leak-me")
	token, err := box.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	// Flip one bit in every position: nonce, ciphertext and tag.
	for i := range token {
		tampered := bytes.Clone(token)
		tampered[i] ^= 0x01
		got, err := box.Decrypt(tampered)
		if !errors.Is(err, ErrDecrypt) {
			t.Fatalf("byte %d flipped: Decrypt = %q, %v; want ErrDecrypt", i, got, err)
		}
		if got != nil {
			t.Errorf("byte %d flipped: Decrypt returned plaintext alongside the error", i)
		}
		assertNoLeak(t, err, plaintext, key, []byte(encoded), tampered)
	}
}

func TestDecryptRejectsWrongKey(t *testing.T) {
	box, key, encoded := newBox(t)
	other, otherKey, otherEncoded := newBox(t)
	plaintext := []byte("do-not-leak-me")
	token, err := box.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	got, err := other.Decrypt(token)
	if !errors.Is(err, ErrDecrypt) {
		t.Fatalf("Decrypt with wrong key = %q, %v; want ErrDecrypt", got, err)
	}
	assertNoLeak(t, err, plaintext, key, []byte(encoded), otherKey, []byte(otherEncoded), token)
}

func TestDecryptRejectsShortToken(t *testing.T) {
	box, _, _ := newBox(t)
	for _, n := range []int{0, 1, 12, 12 + 15} {
		token := bytes.Repeat([]byte{0xab}, n)
		if _, err := box.Decrypt(token); !errors.Is(err, ErrMalformed) {
			t.Errorf("Decrypt(%d bytes) error = %v, want ErrMalformed", n, err)
		}
	}
}

func TestNewRejectsBadKey(t *testing.T) {
	key, _ := newKey(t)
	tests := []struct {
		name    string
		encoded string
		want    error
	}{
		{"empty", "", ErrKeyLength},
		{"not base64", "not*base64!" + strings.Repeat("A", 33), ErrKeyEncoding},
		{"url-safe alphabet", strings.Repeat("_-", 22), ErrKeyEncoding},
		{"unpadded", base64.RawStdEncoding.EncodeToString(key), ErrKeyEncoding},
		{"raw key bytes", string(key), ErrKeyEncoding},
		{"too short", base64.StdEncoding.EncodeToString(key[:31]), ErrKeyLength},
		{"too long", base64.StdEncoding.EncodeToString(append(bytes.Clone(key), 0x00)), ErrKeyLength},
		{"AES-128 length", base64.StdEncoding.EncodeToString(key[:16]), ErrKeyLength},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			box, err := New(tt.encoded)
			if !errors.Is(err, tt.want) {
				t.Fatalf("New error = %v, want %v", err, tt.want)
			}
			if box != nil {
				t.Error("New returned a Box alongside the error")
			}
			assertNoLeak(t, err, key, []byte(tt.encoded))
		})
	}
}

func TestNewAcceptsBase64Key(t *testing.T) {
	key, encoded := newKey(t)
	box, err := New(encoded)
	if err != nil {
		t.Fatal(err)
	}
	// A Box built from the same key opens tokens sealed by the first.
	token, err := box.Encrypt([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := again.Decrypt(token); err != nil {
		t.Errorf("same key failed to decrypt: %v", err)
	}
}
