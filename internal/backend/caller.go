package backend

import "context"

type callerKey struct{}

// Caller is the person a request acts for, when the console acts as each
// caller instead of as itself. Token is their ateapi bearer token.
type Caller struct {
	Token string
	// Atespace is an atespace the caller asked about. For a caller who
	// can't list atespaces, it is read along with the configured ones.
	Atespace string
}

// WithCaller returns ctx carrying the caller.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom returns the caller in ctx, if any.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok && c.Token != ""
}

// WithAtespace records the atespace a request is about, keeping the caller.
func WithAtespace(ctx context.Context, atespace string) context.Context {
	if atespace == "" {
		return ctx
	}
	c, _ := ctx.Value(callerKey{}).(Caller)
	c.Atespace = atespace
	return context.WithValue(ctx, callerKey{}, c)
}

// AtespaceFrom returns the atespace WithAtespace recorded, if any.
func AtespaceFrom(ctx context.Context) string {
	c, _ := ctx.Value(callerKey{}).(Caller)
	return c.Atespace
}
