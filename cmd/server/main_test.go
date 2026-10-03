package main

import (
	"bytes"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/config"
)

func TestLogListeningOmitsURLCredentials(t *testing.T) {
	t.Parallel()

	// Load rejects such URLs; this proves the log stays safe even if one
	// reached the Config some other way.
	u, err := url.Parse("https://alice:hunter2@meals.example.com/app?token=t0ken")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	logListening(slog.New(slog.NewJSONHandler(&buf, nil)), config.Config{HTTPAddr: ":8080", PublicURL: u})

	out := buf.String()
	for _, leak := range []string{"alice", "hunter2", "t0ken"} {
		if strings.Contains(out, leak) {
			t.Errorf("startup log leaks %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, `"public_origin":"https://meals.example.com"`) {
		t.Errorf("startup log missing public origin: %s", out)
	}
}
