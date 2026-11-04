package baton

import (
	"slices"
	"strings"
	"testing"
)

// scrubStringFullScan is scrubString without the mayMatch checks.
func scrubStringFullScan(s string) string {
	for _, r := range scrubRules {
		if r.re.MatchString(s) {
			s = r.re.ReplaceAllLiteralString(s, r.replacement)
		}
	}
	return redactCards(s)
}

var (
	helloText = strings.Repeat("hello ", 51200/6+1)[:51200]
	mixedText = strings.Repeat(`{"id": 48213, "name": "order 2026-11-03", "owner": "ops team", "total": 1299.50, "note": "shipped via UPS"}, `, 51200/100+1)[:51200]
)

// Near misses around each check; precheckInputs adds matchedBy's inputs.
var nearMisses = []string{
	"eyJ", "Bearer abcdefghijklmnopqrstuv", "BEARER\u00a0abcdefghijklmnopqrstuv",
	"bearer\tTOKENTOKENTOKENTOKEN", "bearebearer abcdefghijklmnopqrstuv",
	"Kelvin \u212a sk-", "@",
	"+1 555.123.4567", "call 5551234567 now", "12 34 56", "4111111111111111",
	"41111111111111112", "411111111111 1111", "",
	"555\u0661234567", "555-123-456\u0661", "be\u0430rer abcdefghijklmnopqrstuv", "\xffeyJa.b.c\xff",
	"\xffBearer abcdefghijklmnopqrstuv", "a\xff@b.co", "411111111111111\u06611",
}

// matchedBy is one input each rule's pattern matches, by replacement.
var matchedBy = map[string]string{
	"[REDACTED:jwt]":     "x eyJa.b.c y",
	"[REDACTED:bearer]":  "bEaReR abcdefghijklmnopqrstuv",
	"[REDACTED:sk_key]":  "sk-abcdefghijklmnopqrstuvwx",
	"[REDACTED:aws_key]": "AKIAABCDEFGHIJKLMNOP",
	"[REDACTED:email]":   "a@b.co",
	"[REDACTED:phone]":   "(555) 123-4567",
}

func precheckInputs() []string {
	out := slices.Clone(nearMisses)
	for _, in := range matchedBy {
		out = append(out, in)
	}
	return out
}

func specCaseStrings(tb testing.TB) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			out = append(out, v)
		case map[string]any:
			for k, item := range v {
				out = append(out, k)
				walk(item)
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	for _, c := range scrubCases(tb) {
		walk(c.Input)
	}
	if len(out) == 0 {
		tb.Fatal("no strings in the scrub cases")
	}
	return out
}

func TestScrubStringMatchesFullScan(t *testing.T) {
	inputs := append(specCaseStrings(t), precheckInputs()...)
	inputs = append(inputs, helloText, mixedText, mixedText+" Bearer abcdefghijklmnopqrstuv a@b.co 4111111111111111")
	for _, in := range inputs {
		if got, want := scrubString(in), scrubStringFullScan(in); got != want {
			t.Errorf("%q:\ngot  %q\nwant %q", in, got, want)
		}
	}
}

func FuzzScrubStringMatchesFullScan(f *testing.F) {
	for _, in := range append(specCaseStrings(f), precheckInputs()...) {
		f.Add(in)
	}
	f.Fuzz(func(t *testing.T, in string) {
		if got, want := scrubString(in), scrubStringFullScan(in); got != want {
			t.Errorf("%q:\ngot  %q\nwant %q", in, got, want)
		}
	})
}

func TestEachPrecheckSkipsOnlyTextItsPatternCannotMatch(t *testing.T) {
	for _, r := range scrubRules {
		in, ok := matchedBy[r.replacement]
		if !ok {
			t.Fatalf("%s: no input in matchedBy", r.replacement)
		}
		if !r.re.MatchString(in) || !r.mayMatch(in) {
			t.Errorf("%s: %q must match and pass mayMatch", r.replacement, in)
		}
		if r.mayMatch(helloText) {
			t.Errorf("%s: mayMatch is true for text it cannot match", r.replacement)
		}
	}
	if !ccMayMatch("4111111111111111") || ccMayMatch(mixedText) {
		t.Error("cc: ccMayMatch must be true for 16 digits and false for text without 13 in a row")
	}
}

func BenchmarkScrubString50KB(b *testing.B) {
	for _, text := range []struct{ name, s string }{{"hello", helloText}, {"mixed", mixedText}} {
		b.Run(text.name+"/full_scan", func(b *testing.B) {
			for b.Loop() {
				scrubStringFullScan(text.s)
			}
		})
		b.Run(text.name+"/prechecked", func(b *testing.B) {
			for b.Loop() {
				scrubString(text.s)
			}
		})
	}
}
