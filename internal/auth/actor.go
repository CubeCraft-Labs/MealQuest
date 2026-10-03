package auth

import (
	"context"
	"slices"
)

// Actor is the signed-in household member a request acts for. Handlers
// record Actor.Subject as updated_by on edits.
type Actor struct {
	Subject string
	Email   string
	Name    string
	Groups  []string
}

// Actor returns the household member s belongs to.
func (s Session) Actor() Actor {
	return Actor{Subject: s.Subject, Email: s.Email, Name: s.Name, Groups: slices.Clone(s.Groups)}
}

type sessionKey struct{}

// WithSession returns a copy of ctx carrying s. The session middleware calls
// it for every authenticated request.
func WithSession(ctx context.Context, s Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

// SessionFrom returns the session WithSession stored in ctx. ok is false when
// ctx has none, which behind the session middleware means a wiring bug.
func SessionFrom(ctx context.Context) (s Session, ok bool) {
	s, ok = ctx.Value(sessionKey{}).(Session)
	return s, ok
}

// ActorFrom returns the actor of the session WithSession stored in ctx. ok is
// false when ctx has none.
func ActorFrom(ctx context.Context) (actor Actor, ok bool) {
	s, ok := SessionFrom(ctx)
	if !ok {
		return Actor{}, false
	}
	return s.Actor(), true
}
