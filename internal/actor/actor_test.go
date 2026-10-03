package actor

import (
	"context"
	"testing"
)

func TestFrom(t *testing.T) {
	t.Parallel()
	if got := From(context.Background()); got != "" {
		t.Errorf("From(empty context) = %q, want empty", got)
	}
	if got := From(With(context.Background(), "sam")); got != "sam" {
		t.Errorf("From(With(sam)) = %q, want sam", got)
	}
}
