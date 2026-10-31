package baton

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// The DSN is the one value a vendor copies from /account:
// https://<key>@<host>/<workspace>/<server>. It is split by hand, the same
// way as baton-sdk's _dsn.py: net/url decodes the key and puts the raw URL,
// key included, in its errors.

var (
	vendorIDPattern  = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,48}$`)
	workspacePattern = regexp.MustCompile(`^ten_(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{32})$`)
	keyRun           = regexp.MustCompile(`baton_(?:pk|sk)_[A-Za-z0-9_%-]{8,}`)
	notInAHost       = regexp.MustCompile(`[\\\s\p{Z}\p{Cc}]`)
)

const (
	publishablePrefix = "baton_pk_"
	secretPrefix      = "baton_sk_"
	bareKeyHint       = "this looks like a bare key, not a DSN — copy the full value from " +
		"/account, which starts with https:// and ends with your server's name"
)

type dsn struct {
	origin   string
	tenantID string
	vendorID string
	key      string
}

// String and GoString leave out the key, so printing a dsn cannot leak it.
func (d dsn) String() string   { return d.origin + "/" + d.tenantID + "/" + d.vendorID }
func (d dsn) GoString() string { return d.String() }

func sweepKeys(s string) string { return keyRun.ReplaceAllLiteralString(s, "<key>") }

// dsnError builds every DSN error, so none can carry the key.
func dsnError(format string, args ...any) error {
	return errors.New("baton: " + sweepKeys(fmt.Sprintf(format, args...)))
}

// splitDSN cuts raw into its scheme (lower case), the part before the path,
// and the path without any query or fragment.
func splitDSN(raw string) (scheme, netloc, path string, ok bool) {
	scheme, rest, ok := strings.Cut(raw, "://")
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	path, _, _ = strings.Cut(rest[end:], "?")
	path, _, _ = strings.Cut(path, "#")
	return strings.ToLower(scheme), rest[:end], path, ok
}

// redactDSN keeps everything in raw but the key, for dsnError's messages.
func redactDSN(raw string) string {
	scheme, netloc, path, ok := splitDSN(raw)
	if !ok {
		return "<dsn>"
	}
	at := strings.LastIndex(netloc, "@")
	if at < 0 {
		return scheme + "://<no key>@" + netloc + path
	}
	return scheme + "://***@" + netloc[at+1:] + path
}

func parseDSN(raw string) (dsn, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return dsn{}, dsnError("the DSN must be a non-empty string")
	}
	if strings.HasPrefix(raw, publishablePrefix) || strings.HasPrefix(raw, secretPrefix) {
		return dsn{}, dsnError("the DSN is not a URL: %s", bareKeyHint)
	}
	safe := redactDSN(raw)
	for _, c := range []struct{ char, name string }{{"\t", "tab"}, {"\r", "carriage return"}, {"\n", "line break"}} {
		if strings.Contains(raw, c.char) {
			return dsn{}, dsnError("DSN %q has a %s inside it — probably a line wrap where the value was copied. "+
				"Paste the value from /account as one line", safe, c.name)
		}
	}
	scheme, netloc, path, ok := splitDSN(raw)
	if !ok || (scheme != "https" && scheme != "http") {
		return dsn{}, dsnError("DSN %s must start with https:// (or http:// for local development) — %s", safe, bareKeyHint)
	}

	at := strings.LastIndex(netloc, "@")
	if at <= 0 {
		if keyRun.MatchString(netloc) && strings.Trim(path, "/") == "" {
			return dsn{}, dsnError("DSN %s is a key with a scheme in front of it, not a DSN — what is missing "+
				"is the rest: an @, the host, and the two path segments, as in https://baton_pk_...@host/ten_.../server", safe)
		}
		detail := "the value from /account has the key before an @"
		if keyRun.MatchString(path) {
			detail = "the key is in the PATH — it goes before an @"
		}
		return dsn{}, dsnError("DSN %s carries no key: %s, as in https://baton_pk_...@host/ten_.../server", safe, detail)
	}
	key, authority := netloc[:at], netloc[at+1:]
	if strings.Contains(key, ":") {
		return dsn{}, dsnError("DSN %s has a ':' in its key — a DSN carries one credential and no password field", safe)
	}
	if err := checkHost(authority, safe); err != nil {
		return dsn{}, err
	}

	segments := strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
	if len(segments) != 2 {
		return dsn{}, dsnError("DSN %s must carry exactly two path segments — the workspace and the server, "+
			"as in /ten_.../<server>. A missing server is never defaulted: it is what the key is bound to", safe)
	}
	workspace, server := segments[0], segments[1]
	for _, slot := range []struct{ name, value string }{{"workspace", workspace}, {"server", server}} {
		if keyRun.MatchString(slot.value) {
			return dsn{}, dsnError("DSN %s has a KEY in the %s slot. The key goes before the @, and the path "+
				"carries the workspace and the server: https://baton_pk_...@host/ten_.../<server>", safe, slot.name)
		}
	}
	if !workspacePattern.MatchString(workspace) {
		return dsn{}, dsnError("DSN %s has %q where the workspace belongs — expected ten_ followed by 8 or 32 hex "+
			"characters. If the two path segments are the right way round, this is not a Baton DSN", safe, workspace)
	}
	if !vendorIDPattern.MatchString(server) {
		return dsn{}, dsnError("DSN %s has %q where the server belongs — it must match %s, because it becomes "+
			"the vendor id and the annotation tool name prefix", safe, server, vendorIDPattern)
	}
	if strings.HasPrefix(key, secretPrefix) {
		slog.Warn("baton: this DSN carries a " + secretPrefix + " key (a workspace SECRET) where a " + publishablePrefix +
			" key belongs. It will work. But if this server ships to anyone else, that key reads and writes " +
			"everything the workspace holds — mint a publishable key at /account instead.")
	}
	origin := scheme + "://" + authority
	if u, err := url.Parse(origin); err != nil || u.Host != authority {
		return dsn{}, dsnError("DSN %s has a host that is not valid in a URL", safe)
	}
	return dsn{origin: origin, tenantID: workspace, vendorID: server, key: key}, nil
}

func checkHost(authority, safe string) error {
	if keyRun.MatchString(authority) {
		return dsnError("DSN %s has the KEY where the host belongs. The order is key, @, host — check whether "+
			"the two are the wrong way round: https://baton_pk_...@host/ten_.../<server>", safe)
	}
	if notInAHost.MatchString(authority) {
		return dsnError("DSN %s has something other than a host between its key and its path. The workspace "+
			"and the server are the two path segments after it: https://baton_pk_...@host/ten_.../server", safe)
	}
	host, port := authority, ""
	if i := strings.LastIndex(authority, ":"); i > strings.LastIndex(authority, "]") {
		host, port = authority[:i], authority[i+1:]
	}
	if strings.Trim(host, "[]") == "" {
		return dsnError("DSN %s has no host", safe)
	}
	if n, err := strconv.Atoi(port); port != "" && (err != nil || n < 0 || n > 65535) {
		return dsnError("DSN %s has a port that is not a number from 0 to 65535 — the authority is a host "+
			"and an optional numeric port, as in host:8443", safe)
	}
	return nil
}
