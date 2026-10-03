package store

import (
	"testing"

	"github.com/oklog/ulid/v2"
)

func TestNewIDStrictlyIncreasing(t *testing.T) {
	const n = 10000
	prev := NewID()
	for range n {
		id := NewID()
		if len(id) != ulid.EncodedSize {
			t.Fatalf("NewID() = %q, want %d characters", id, ulid.EncodedSize)
		}
		if _, err := ulid.ParseStrict(id); err != nil {
			t.Fatalf("NewID() = %q: %v", id, err)
		}
		if id <= prev {
			t.Fatalf("NewID() = %q, not after previous %q", id, prev)
		}
		prev = id
	}
}
