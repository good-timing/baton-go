package baton

import (
	"context"
	"log/slog"
	"strings"
	"unicode/utf8"

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

const identityCap = 128

// usableIdentityText is SPEC §11.4's test: not blank, well-formed, no U+0000.
func usableIdentityText(s string) bool {
	return strings.TrimSpace(s) != "" && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// wireIdentity sends id as the resolver returned it, cut at identityCap code
// points as the reference producers do; an unusable id gives no identity, and
// a display name past the cap is dropped (SPEC §11.4).
func wireIdentity(id, displayName string, hashed bool) *identityWire {
	if !usableIdentityText(id) {
		return nil
	}
	id = cutRunes(id, identityCap)
	form := "raw"
	if hashed {
		form = "hashed"
	}
	w := &identityWire{ID: id, Source: "asserted", Form: form}
	if usableIdentityText(displayName) && utf8.RuneCountInString(displayName) <= identityCap {
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
