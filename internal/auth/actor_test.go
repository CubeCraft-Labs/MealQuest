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
	if _, ok := SessionFrom(context.Background()); ok {
		t.Error("SessionFrom found a session in an empty context")
	}
	s := Session{Subject: "user-123", Email: "cook@example.com", Name: "Home Cook", Groups: []string{"mealquest-admin"}, CSRFToken: "t"}
	ctx := WithSession(context.Background(), s)
	gotSession, ok := SessionFrom(ctx)
	if !ok || gotSession.Subject != s.Subject || gotSession.CSRFToken != s.CSRFToken {
		t.Errorf("SessionFrom = %+v (ok=%v), want %+v", gotSession, ok, s)
	}
	got, ok := ActorFrom(ctx)
	if !ok || got.Subject != s.Subject || got.Email != s.Email || got.Name != s.Name || !slices.Equal(got.Groups, s.Groups) {
		t.Errorf("ActorFrom = %+v (ok=%v), want the session's actor", got, ok)
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
