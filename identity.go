package baton

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"
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
	// Labels are your names for the groups this caller is in, for the
	// Console's settings to match (SPEC §11.4). They are sent as given, not
	// scrubbed, so use group names or keys you keep, never raw ids. A
	// Principal with Labels and no ID sends the labels alone. Sent with the
	// Organization's Labels.
	//
	// Experimental: may change in a minor release.
	Labels []string
}

type Organization struct {
	ID          string
	DisplayName string
	Hashed      bool
	// Labels are as Principal.Labels.
	//
	// Experimental: may change in a minor release.
	Labels []string
}

// ResolvePrincipal names the caller of one request. Return nil for "no one".
// ctx ends with the request, after 5 seconds, or once Baton has the answer.
type ResolvePrincipal func(ctx context.Context, c Caller) (*Principal, error)

// Caller is what ResolvePrincipal is given about a request.
type Caller struct {
	// Token is a copy of the request's verified token: nil on stdio and SSE,
	// and on HTTP without go-sdk's auth.RequireBearerToken. Change none of
	// it: a nested claim is the request's own.
	Token *auth.TokenInfo
	// Header is nil on stdio and SSE, which pass no headers; an empty Header
	// means the client sent none.
	Header http.Header
	Meta   map[string]any
	// ToolName is "" on a request that is not a tool call.
	ToolName string
	// Arguments are the tool's, without user_goal unless a tool of yours
	// declares it; on Baton's report tool, the report. A number is a
	// json.Number.
	Arguments map[string]any
}

// PrincipalFromOAuthSub names the caller by the token's "sub" claim, in
// Token.Extra. sub is unique per issuer only.
func PrincipalFromOAuthSub(_ context.Context, c Caller) (*Principal, error) {
	if sub := claim(c, "sub"); sub != "" {
		return &Principal{ID: sub}, nil
	}
	return nil, nil
}

// PrincipalFromOAuthEmail names the caller by the token's "email" claim, in
// Token.Extra: the whole address is the ID, and the part before "@" is the
// display name. Most identity providers put email in the ID token, not the
// access token.
func PrincipalFromOAuthEmail(_ context.Context, c Caller) (*Principal, error) {
	email := claim(c, "email")
	if email == "" {
		return nil, nil
	}
	p := &Principal{ID: email}
	if at := strings.LastIndex(email, "@"); at > 0 {
		p.DisplayName = email[:at]
	}
	return p, nil
}

func claim(c Caller, name string) string {
	if c.Token == nil {
		return ""
	}
	if v, _ := c.Token.Extra[name].(string); strings.TrimSpace(v) != "" {
		return v
	}
	return ""
}

const identityCap = 128

// usableIdentityText is SPEC §11.4's test: not blank, well-formed, no U+0000.
func usableIdentityText(s string) bool {
	return strings.TrimSpace(s) != "" && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

// wireIdentity sends id as the resolver returned it, cut at identityCap code
// points as the reference producers do; an unusable id gives no identity, and
// a display name past the cap is dropped (SPEC §11.4).
func wireIdentity(id, displayName string, hashed bool) *Identity {
	if !usableIdentityText(id) {
		return nil
	}
	id = cutRunes(id, identityCap)
	form := "raw"
	if hashed {
		form = "hashed"
	}
	w := &Identity{ID: id, Source: "asserted", Form: form}
	if usableIdentityText(displayName) && utf8.RuneCountInString(displayName) <= identityCap {
		w.DisplayName = &displayName
	}
	return w
}

// hookTimeout is how long a call waits for ResolvePrincipal, as in baton-sdk.
var hookTimeout = 5 * time.Second

// hookCeiling is how many timed-out resolvers may still be running in this
// process: one that ignores ctx keeps its goroutine.
const hookCeiling = 64

var stuckHooks atomic.Int64

// resolveCaller never fails or holds up the call: a resolver that errors,
// panics or takes longer than hookTimeout leaves the event with no principal
// (SPEC §11.2). While hookCeiling timed-out ones still run, it is not called
// (TestOnlyTimedOutResolversCountTowardTheCeiling); calls already waiting can
// still add to them.
func (b *installed) resolveCaller(ctx context.Context, caller func() Caller) (w who) {
	defer func() {
		if r := recover(); r != nil {
			logRecovered("baton: building the principal failed; the event carries no principal", r)
			w = who{}
		}
	}()
	if stuckHooks.Load() >= hookCeiling {
		b.hooksStuckWarned.Do(func() {
			slog.Warn("baton: ResolvePrincipal calls are still running long after the limit, so it is not called until one ends; later warnings are not logged", "running", hookCeiling)
		})
		return who{}
	}
	if ctx.Err() != nil {
		return who{}
	}
	c := caller()
	hookCtx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	type answer struct {
		p   *Principal
		err error
	}
	// Buffered, so a resolver that finishes after the timeout does not block.
	done := make(chan answer, 1)
	// The side that moves state off open decides the call: an answer in time,
	// or a resolver given up on, which counts as stuck until it returns.
	const (
		open = iota
		answered
		stuck
	)
	var state atomic.Int32
	go func() {
		var a answer
		// Deferred, so a resolver that panics or exits its goroutine still answers.
		defer func() {
			r := recover()
			if r != nil {
				a = answer{}
			}
			if state.CompareAndSwap(open, answered) {
				done <- a
			} else if state.Load() == stuck {
				stuckHooks.Add(-1)
			}
			if r != nil {
				logRecovered("baton: ResolvePrincipal panicked; the event carries no principal", r)
			}
		}()
		a.p, a.err = b.cfg.ResolvePrincipal(hookCtx, c)
	}()
	var a answer
	select {
	case a = <-done:
	case <-hookCtx.Done():
		if state.CompareAndSwap(open, stuck) {
			stuckHooks.Add(1)
			b.warnSlowHook(ctx)
			return who{}
		}
		a = <-done
	}
	if a.err != nil {
		// A resolver that stops for ctx is a timeout or a cancel, not a failure.
		if hookCtx.Err() != nil && errors.Is(a.err, hookCtx.Err()) {
			b.warnSlowHook(ctx)
		} else {
			slog.Warn("baton: ResolvePrincipal failed; the event carries no principal", "error", a.err)
		}
		return who{}
	}
	if a.p == nil {
		return who{}
	}
	w.principal = wireIdentity(a.p.ID, a.p.DisplayName, a.p.Hashed)
	var orgLabels []string
	if o := a.p.Organization; o != nil {
		w.org = wireIdentity(o.ID, o.DisplayName, o.Hashed)
		orgLabels = o.Labels
	}
	w.labels = cleanLabels(a.p.Labels, orgLabels)
	return w
}

// who is the caller as sent: principal, organization and labels.
type who struct {
	principal, org *Identity
	labels         []string
}

const (
	labelsCap   = 20
	labelLength = 64
)

// cleanLabels keeps the usable labels in order, without repeats, at most
// labelsCap; nil when none are left, as "labels" is never [] (SPEC §11.4).
func cleanLabels(given ...[]string) []string {
	var out []string
	for _, list := range given {
		for _, l := range list {
			if len(out) == labelsCap {
				return out
			}
			if usableIdentityText(l) && utf8.RuneCountInString(l) <= labelLength && !slices.Contains(out, l) {
				out = append(out, l)
			}
		}
	}
	return out
}

// logRecovered logs a recovered panic. A log handler that panics on it would
// otherwise crash the process from the hook's goroutine, which nothing guards.
func logRecovered(msg string, r any) {
	defer func() { _ = recover() }()
	slog.Error(msg, "panic", r)
}

// warnSlowHook warns once, unless the request itself was cancelled.
func (b *installed) warnSlowHook(request context.Context) {
	if request.Err() != nil {
		return
	}
	b.hookSlowWarned.Do(func() {
		slog.Warn("baton: ResolvePrincipal took longer than the limit; the event carries no principal, and later warnings are not logged", "limit", hookTimeout)
	})
}
