package main

import (
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/config"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/vapid"
)

// TestRunPrintsAMatchingPair checks the output shape and that the printed
// private key line is a key config.Load accepts for the printed public key.
func TestRunPrintsAMatchingPair(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	if err := run(&out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "# ") || !strings.HasPrefix(lines[2], "# ") {
		t.Fatalf("output = %q, want comment, public key, comment, private key", out.String())
	}
	public, ok := strings.CutPrefix(lines[1], config.EnvVAPIDPublicKey+"=")
	if !ok {
		t.Fatalf("public key line = %q, want %s=<key>", lines[1], config.EnvVAPIDPublicKey)
	}
	pub, err := vapid.ParsePublicKey(public)
	if err != nil {
		t.Fatalf("printed public key: %v", err)
	}
	priv, err := vapid.ParsePrivateKey(lines[3])
	if err != nil {
		t.Fatalf("printed private key: %v", err)
	}
	if err := vapid.CheckPair(pub, priv); err != nil {
		t.Error(err)
	}

	var second strings.Builder
	if err := run(&second); err != nil {
		t.Fatal(err)
	}
	if second.String() == out.String() {
		t.Error("two runs printed the same key pair")
	}
}
