package vapid

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerateRoundTrips(t *testing.T) {
	t.Parallel()
	a, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two generated key pairs are equal")
	}
	// 65 and 32 bytes in unpadded base64url.
	if len(a.PublicKey) != 87 || len(a.PrivateKey) != 43 {
		t.Errorf("key lengths = %d, %d; want 87, 43", len(a.PublicKey), len(a.PrivateKey))
	}
	pub, err := ParsePublicKey(a.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := ParsePrivateKey(a.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckPair(pub, priv); err != nil {
		t.Errorf("CheckPair of a generated pair: %v", err)
	}
	other, err := ParsePrivateKey(b.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckPair(pub, other); err != ErrMismatch {
		t.Errorf("CheckPair of mismatched keys = %v, want ErrMismatch", err)
	}
}

func TestParseRejectsMalformedKeys(t *testing.T) {
	t.Parallel()
	pair, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	raw := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	compressed, _ := base64.RawURLEncoding.DecodeString(pair.PublicKey)
	compressed = append([]byte{0x02}, compressed[1:33]...)
	offCurve := make([]byte, 65)
	offCurve[0] = 0x04
	offCurve[64] = 1
	order := []byte{ // the P-256 group order n, not a valid scalar
		0xff, 0xff, 0xff, 0xff, 0x00, 0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		0xbc, 0xe6, 0xfa, 0xad, 0xa7, 0x17, 0x9e, 0x84, 0xf3, 0xb9, 0xca, 0xc2, 0xfc, 0x63, 0x25, 0x51,
	}

	for name, s := range map[string]string{
		"empty":          "",
		"padded":         pair.PublicKey + "=",
		"standard alpha": strings.NewReplacer("-", "+", "_", "/").Replace(pair.PublicKey) + "+/",
		"compressed":     raw(compressed),
		"off curve":      raw(offCurve),
		"private key":    pair.PrivateKey,
	} {
		if _, err := ParsePublicKey(s); err == nil {
			t.Errorf("ParsePublicKey(%s) succeeded", name)
		}
	}
	for name, s := range map[string]string{
		"empty":      "",
		"padded":     pair.PrivateKey + "=",
		"PEM":        "-----BEGIN EC PRIVATE KEY-----",
		"zero":       raw(make([]byte, 32)),
		"group size": raw(order),
		"public key": pair.PublicKey,
	} {
		_, err := ParsePrivateKey(s)
		if err == nil {
			t.Errorf("ParsePrivateKey(%s) succeeded", name)
			continue
		}
		if s != "" && strings.Contains(err.Error(), s) {
			t.Errorf("ParsePrivateKey(%s) error quotes the key: %v", name, err)
		}
	}
}
