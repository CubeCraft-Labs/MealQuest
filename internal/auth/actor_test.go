package auth

import (
	"context"
	"slices"
	"testing"
)

func TestActorContext(t *testing.T) {
	t.Parallel()
	if _, ok := ActorFrom(context.Background()); ok {
		t.Error("ActorFrom found an actor in an empty context")
	}
	want := Actor{Subject: "user-123", Email: "cook@example.com", Name: "Home Cook", Groups: []string{"mealquest-admin"}}
	got, ok := ActorFrom(WithActor(context.Background(), want))
	if !ok || got.Subject != want.Subject || got.Email != want.Email || got.Name != want.Name || !slices.Equal(got.Groups, want.Groups) {
		t.Errorf("ActorFrom = %+v (ok=%v), want %+v", got, ok, want)
	}
}

func TestSessionActor(t *testing.T) {
	t.Parallel()
	s := Session{Subject: "user-123", Email: "cook@example.com", Name: "Home Cook", Groups: []string{"mealquest-admin"}, CSRFToken: "t"}
	a := s.Actor()
	if a.Subject != s.Subject || a.Email != s.Email || a.Name != s.Name || !slices.Equal(a.Groups, s.Groups) {
		t.Errorf("Actor = %+v, want the session's identity", a)
	}
	a.Groups[0] = "changed"
	if s.Groups[0] != "mealquest-admin" {
		t.Error("Actor shares the session's groups slice")
	}
}
