package baton

import (
	"context"
	"log/slog"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

// Principal is who made the call, as the vendor's resolver names them
// (SPEC §11.4). A company goes in Organization.
type Principal struct {
	ID          string
	DisplayName string
	// Hashed says ID is a pseudonym the resolver derived. Leave it false for
	// a real identity: a consumer treats anything not hashed as personal data.
	Hashed       bool
	Organization *Organization
}

type Organization struct {
	ID          string
	DisplayName string
	Hashed      bool
}

// ResolvePrincipal names the caller from the request's verified token, which
// is nil when the transport carries none. Return nil for "no one".
type ResolvePrincipal func(ctx context.Context, token *auth.TokenInfo) (*Principal, error)

func wireIdentity(id, displayName string, hashed bool) *identityWire {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	form := "raw"
	if hashed {
		form = "hashed"
	}
	w := &identityWire{ID: id, Source: "asserted", Form: form}
	if strings.TrimSpace(displayName) != "" {
		w.DisplayName = &displayName
	}
	return w
}

// resolveCaller never fails the call: a resolver that errors or panics
// leaves the event with no principal (SPEC §11.2).
func resolveCaller(ctx context.Context, hook ResolvePrincipal, token *auth.TokenInfo) (principal, org *identityWire) {
	if hook == nil {
		return nil, nil
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("baton: ResolvePrincipal panicked; the event carries no principal", "panic", r)
			principal, org = nil, nil
		}
	}()
	p, err := hook(ctx, token)
	if err != nil {
		slog.Warn("baton: ResolvePrincipal failed; the event carries no principal", "error", err)
		return nil, nil
	}
	if p == nil {
		return nil, nil
	}
	principal = wireIdentity(p.ID, p.DisplayName, p.Hashed)
	if p.Organization != nil {
		org = wireIdentity(p.Organization.ID, p.Organization.DisplayName, p.Organization.Hashed)
	}
	return principal, org
}
