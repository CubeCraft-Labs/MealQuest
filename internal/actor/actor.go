// Package actor carries the signed-in member's name in a request context, so
// changes can record who made them (the updated_by columns).
package actor

import "context"

type key struct{}

// With returns a copy of ctx naming the member making the request.
func With(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, key{}, name)
}

// From returns the member named in ctx, or "" when no member is known.
func From(ctx context.Context) string {
	name, _ := ctx.Value(key{}).(string)
	return name
}
