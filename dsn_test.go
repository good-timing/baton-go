package baton

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// The cases follow baton-sdk's tests/test_dsn.py.

const (
	testWorkspace = "ten_655b084e118b43f88992ee6357fcc23c"
	testKey       = "baton_pk_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testDSN       = "https://" + testKey + "@ingest.goodtiming.ai/" + testWorkspace + "/echo-server"
)

func TestParseDSN(t *testing.T) {
	got, err := parseDSN(testDSN)
	if err != nil {
		t.Fatal(err)
	}
	want := dsn{origin: "https://ingest.goodtiming.ai", tenantID: testWorkspace, vendorID: "echo-server", key: testKey}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseDSNAccepts(t *testing.T) {
	for _, c := range []struct{ name, raw, origin string }{
		{"trailing slash", testDSN + "/", "https://ingest.goodtiming.ai"},
		{"http for local development", "http://" + testKey + "@localhost/" + testWorkspace + "/srv", "http://localhost"},
		{"port", "http://" + testKey + "@localhost:8080/" + testWorkspace + "/srv", "http://localhost:8080"},
		{"ipv6 with port", "http://" + testKey + "@[::1]:8080/" + testWorkspace + "/srv", "http://[::1]:8080"},
		{"short workspace", "https://" + testKey + "@h.example.com/ten_0123abcd/srv", "https://h.example.com"},
		{"spaces around it", "  " + testDSN + "  ", "https://ingest.goodtiming.ai"},
		{"upper-case scheme", "HTTPS://" + testKey + "@h.example.com/" + testWorkspace + "/srv", "https://h.example.com"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseDSN(c.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got.origin != c.origin {
				t.Errorf("origin = %q, want %q", got.origin, c.origin)
			}
		})
	}
}

func TestParseDSNRefuses(t *testing.T) {
	const ws = testWorkspace
	for _, c := range []struct{ name, raw, says string }{
		{"empty", "", "non-empty"},
		{"a bare publishable key", testKey, "bare key"},
		{"a bare secret key", "baton_sk_" + strings.Repeat("c", 43), "bare key"},
		{"no scheme", "ingest.goodtiming.ai/" + ws + "/srv", "https://"},
		{"wrong scheme", "ftp://" + testKey + "@h.example.com/" + ws + "/srv", "https://"},
		{"no key", "https://h.example.com/" + ws + "/srv", "carries no key"},
		{"password slot", "https://" + testKey + ":secret@h.example.com/" + ws + "/srv", "no password field"},
		{"no server segment", "https://" + testKey + "@h.example.com/" + ws, "exactly two path segments"},
		{"three segments", "https://" + testKey + "@h.example.com/" + ws + "/srv/extra", "exactly two path segments"},
		{"swapped segments", "https://" + testKey + "@h.example.com/srv/" + ws, "where the workspace belongs"},
		{"bad server name", "https://" + testKey + "@h.example.com/" + ws + "/my.server", "where the server belongs"},
		{"key in the authority", "https://" + testKey, "a key with a scheme in front of it"},
		{"key in the authority, trailing slash", "https://" + testKey + "/", "a key with a scheme in front of it"},
		{"key in the path", "https://h.example.com/" + testKey + "/srv", "the key is in the PATH"},
		{"key glued to the server", "https://" + testKey + "@h.example.com/" + ws + "/srv-" + testKey, "KEY in the server slot"},
		{"key glued to the workspace", "https://" + testKey + "@h.example.com/tenant-" + testKey + "/srv", "KEY in the workspace slot"},
		{"key as the workspace", "https://" + testKey + "@h.example.com/" + testKey + "/srv", "KEY in the workspace slot"},
		{"backslash in the host", "https://" + testKey + "@h.example.com\\evil.com/" + ws + "/srv", "something other than a host"},
		{"line break in the host", "https://" + testKey + "@h.example.com\nevil.com/" + ws + "/srv", "line break"},
		{"port that is not a number", "https://" + testKey + "@h.example.com:notaport/" + ws + "/srv", "port that is not a number"},
		{"port out of range", "https://" + testKey + "@h.example.com:99999/" + ws + "/srv", "port that is not a number"},
		{"no-break space after the host", "https://" + testKey + "@h.example.com\u00a0/" + ws + "/srv", "something other than a host"},
		{"unclosed bracket", "https://" + testKey + "@[::1/" + ws + "/srv", "not valid in a URL"},
		{"bad escape in the host", "https://" + testKey + "@ho%zzst/" + ws + "/srv", "not valid in a URL"},
		{"key in the host behind userinfo", "https://x@" + testKey + "/" + ws + "/srv", "KEY where the host belongs"},
		{"key and host swapped", "https://h.example.com@" + testKey + "/" + ws + "/srv", "KEY where the host belongs"},
		{"no host", "https://" + testKey + "@/" + ws + "/srv", "no host"},
		{"non-hex workspace", "https://" + testKey + "@h.example.com/ten_" + strings.Repeat("g", 8) + "/srv", "where the workspace belongs"},
		{"49-character server", "https://" + testKey + "@h.example.com/" + ws + "/" + strings.Repeat("s", 49), "where the server belongs"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseDSN(c.raw)
			if err == nil {
				t.Fatal("parsed")
			}
			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("error %q does not say %q", err, c.says)
			}
			if strings.Contains(err.Error(), "baton_pk_aaaa") || strings.Contains(err.Error(), "baton_sk_cccc") {
				t.Errorf("error repeats the key: %q", err)
			}
		})
	}
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func TestASecretKeyWarnsWithoutRepeatingIt(t *testing.T) {
	logs := captureLogs(t)
	secret := "baton_sk_" + strings.Repeat("c", 43)
	if _, err := parseDSN(strings.Replace(testDSN, testKey, secret, 1)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "SECRET") || strings.Contains(logs.String(), secret) {
		t.Fatalf("logs: %s", logs)
	}
	logs.Reset()
	if _, err := parseDSN(testDSN); err != nil || logs.Len() != 0 {
		t.Fatalf("a publishable key: err %v, logs %s", err, logs)
	}
}

func TestPrintingADSNLeavesOutTheKey(t *testing.T) {
	d, err := parseDSN(testDSN)
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"%v", "%+v", "%s", "%#v"} {
		if got := fmt.Sprintf(verb, d); strings.Contains(got, testKey) {
			t.Errorf("%s prints the key: %s", verb, got)
		}
	}
}

func TestRedactDSN(t *testing.T) {
	if got := redactDSN(testDSN); got != "https://***@ingest.goodtiming.ai/"+testWorkspace+"/echo-server" {
		t.Errorf("got %q", got)
	}
	if got := redactDSN("x@" + testKey + "@y"); got != "<dsn>" {
		t.Errorf("got %q", got)
	}
}
