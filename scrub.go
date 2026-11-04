package baton

import (
	"log/slog"
	"regexp"
	"strings"
	"sync"
)

// The SPEC §7 default ruleset, ported from baton-sdk's scrub.py. RE2's \d and
// \b are ASCII already, which is what SPEC §7 fixes them to.

const (
	depthLimit  = 10
	depthMarker = "[REDACTED:depth-limit]"
	wsChars     = ` \t\n\r\f\x{00a0}`
)

var redactFieldNames = map[string]bool{
	"email": true, "phone": true, "ssn": true, "api_key": true,
	"token": true, "secret": true, "password": true, "user_name": true,
}

type scrubRule struct {
	re          *regexp.Regexp
	replacement string
	// mayMatch is false only for a string re cannot match
	// (TestScrubStringMatchesFullScan). Go's regexp scans a long string
	// slowly when nothing matches; mayMatch skips that scan.
	mayMatch func(string) bool
}

func rule(category, pattern string, mayMatch func(string) bool) scrubRule {
	return scrubRule{regexp.MustCompile(pattern), "[REDACTED:" + category + "]", mayMatch}
}

func contains(sub string) func(string) bool {
	return func(s string) bool { return strings.Contains(s, sub) }
}

// Order matters where patterns overlap: jwt before bearer, keys before email.
var scrubRules = []scrubRule{
	rule("jwt", `\beyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\b`, contains("eyJ")),
	// Only the word ignores case: (?i) on the token class would also match
	// ſ and K (Kelvin). TestScrubberMatchesBatonSDKOnTheSpecCases pins it.
	rule("bearer", `\b(?i:bearer)[`+wsChars+`]+[A-Za-z0-9_\-.+/=]{16,}`, containsBearer),
	rule("sk_key", `\bsk-[A-Za-z0-9_\-]{20,}\b`, contains("sk-")),
	rule("aws_key", `\bAKIA[0-9A-Z]{16}\b`, contains("AKIA")),
	rule("email", `\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`, contains("@")),
	rule("phone", `\b(?:\+?1[`+wsChars+`.\-]?)?\(?\d{3}\)?[`+wsChars+`.\-]?\d{3}[`+wsChars+`.\-]?\d{4}\b`, digitRun(4)),
}

var (
	ccCandidate = regexp.MustCompile(`\b\d{13,19}\b`)
	ccMayMatch  = digitRun(13)
)

// containsBearer is "bearer" in any ASCII case, all that (?i:bearer) matches:
// none of its letters folds to a non-ASCII rune.
func containsBearer(s string) bool {
	const word = "bearer"
outer:
	for i := 0; i+len(word) <= len(s); i++ {
		for j := range len(word) {
			if s[i+j]|0x20 != word[j] {
				continue outer
			}
		}
		return true
	}
	return false
}

// digitRun is whether a string has n ASCII digits in a row, as \d{n} needs.
func digitRun(n int) func(string) bool {
	return func(s string) bool {
		run := 0
		for i := 0; i < len(s); i++ {
			if '0' <= s[i] && s[i] <= '9' {
				run++
				if run >= n {
					return true
				}
			} else {
				run = 0
			}
		}
		return false
	}
}

func luhnValid(digits string) bool {
	total := 0
	for i := 0; i < len(digits); i++ {
		n := int(digits[len(digits)-1-i] - '0')
		if i%2 == 1 {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		total += n
	}
	return total%10 == 0
}

// DefaultScrubber redacts the SPEC §7 patterns in a JSON-shaped value
// (maps, slices, strings), and every string under a sensitive key.
func DefaultScrubber(value any) any {
	return scrubWalk(value, 0, "")
}

// NoScrub turns scrubbing off: pass it as Config.Scrubber.
func NoScrub(value any) any { return value }

var depthWarned sync.Once

func scrubWalk(value any, depth int, forceField string) any {
	switch v := value.(type) {
	case map[string]any:
		if depth >= depthLimit {
			return pastDepthLimit()
		}
		out := make(map[string]any, len(v))
		for k, item := range v {
			field := forceField
			if lower := lowerLikePython(k); redactFieldNames[lower] {
				field = lower
			}
			out[k] = scrubWalk(item, depth+1, field)
		}
		return out
	case []any:
		if depth >= depthLimit {
			return pastDepthLimit()
		}
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = scrubWalk(item, depth+1, forceField)
		}
		return out
	case string:
		if forceField != "" {
			return "[REDACTED:field-" + forceField + "]"
		}
		return scrubString(v)
	}
	return value
}

func pastDepthLimit() string {
	depthWarned.Do(func() {
		slog.Warn("baton: a payload nests deeper than the limit; deeper containers are sent as a marker",
			"limit", depthLimit, "marker", depthMarker)
	})
	return depthMarker
}

func scrubString(s string) string {
	// Most strings match nothing; MatchString does not copy them.
	for _, r := range scrubRules {
		if r.mayMatch(s) && r.re.MatchString(s) {
			s = r.re.ReplaceAllLiteralString(s, r.replacement)
		}
	}
	if !ccMayMatch(s) {
		return s
	}
	return redactCards(s)
}

func redactCards(s string) string {
	if !ccCandidate.MatchString(s) {
		return s
	}
	return ccCandidate.ReplaceAllStringFunc(s, func(m string) string {
		if luhnValid(m) {
			return "[REDACTED:cc]"
		}
		return m
	})
}
