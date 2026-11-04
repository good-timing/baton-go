package baton

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The settings document tests are baton-sdk's tests/test_capture_settings.py,
// case for case, so both SDKs match a caller the same way.

func settingsBody(t *testing.T, version int, audiences ...map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"version": version, "audiences": audiences})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func anAudience(id string, match map[string]any, results any) map[string]any {
	return map[string]any{"id": id, "match": match, "capture": map[string]any{"goal": "required", "reports": "off", "results": results}}
}

var everyone = anAudience("everyone", map[string]any{}, "off")

func parsed(t *testing.T, body []byte) *settingsDocument {
	t.Helper()
	d := parseSettings(body)
	if d == nil {
		t.Fatalf("not a valid document: %s", body)
	}
	return d
}

func matched(t *testing.T, body []byte, v matchView) string {
	t.Helper()
	return parsed(t, body).settingsFor(v, "srv").capture.Audience
}

func TestTheShareBucketIsPinned(t *testing.T) {
	// SPEC §2.6's formula; baton-sdk pins the same buckets.
	for key, want := range map[string]int{"alice": 1556, "bob": 3902, "carol": 7404} {
		if got := shareBucket("acme", key); got != want {
			t.Errorf("%s: bucket %d, want %d", key, got, want)
		}
	}
}

func TestAHalfShareRoundsUp(t *testing.T) {
	// 0.00025 × 10000 is exactly 2.5, so buckets 0, 1 and 2 are in.
	key := ""
	for i := 0; key == ""; i++ {
		if k := fmt.Sprintf("u%d", i); shareBucket("srv", k) == 2 {
			key = k
		}
	}
	body := settingsBody(t, 3, anAudience("tiny", map[string]any{"share": 0.00025}, "full"), everyone)
	if got := matched(t, body, matchView{shareKey: key}); got != "tiny" {
		t.Errorf("bucket 2 matched %q", got)
	}
}

func TestRaisingTheShareKeepsEveryoneAlreadyIn(t *testing.T) {
	small := settingsBody(t, 3, anAudience("in", map[string]any{"share": 0.1}, "full"), everyone)
	large := settingsBody(t, 3, anAudience("in", map[string]any{"share": 0.5}, "full"), everyone)
	inSmall, inLarge := 0, 0
	for i := range 500 {
		v := matchView{shareKey: fmt.Sprintf("user-%d", i)}
		s, l := matched(t, small, v) == "in", matched(t, large, v) == "in"
		if s && !l {
			t.Fatalf("%s left the audience when the share grew", v.shareKey)
		}
		if s {
			inSmall++
		}
		if l {
			inLarge++
		}
	}
	if inSmall <= 25 || inSmall >= 75 || inLarge <= inSmall {
		t.Errorf("%d in at 0.1, %d at 0.5", inSmall, inLarge)
	}
}

func TestShareNeedsAnIdentity(t *testing.T) {
	body := settingsBody(t, 3, anAudience("all", map[string]any{"share": 1}, "full"), everyone)
	if got := matched(t, body, matchView{}); got != "everyone" {
		t.Errorf("no identity matched %q", got)
	}
	if got := matched(t, body, matchView{shareKey: "user-1"}); got != "all" {
		t.Errorf("an identity matched %q", got)
	}
}

func TestAnInvalidDocumentIsRefused(t *testing.T) {
	for _, body := range []string{
		"not json", "[]", strings.Repeat("[", 100000), "null", `{"version": 3, "audiences": [{"id": "a", "match": {}, "capture": {}}]} x`,
		`{"version": 0, "audiences": [{"id": "a", "match": {}, "capture": {}}]}`,
		`{"version": true, "audiences": [{"id": "a", "match": {}, "capture": {}}]}`,
		`{"version": 1.5, "audiences": [{"id": "a", "match": {}, "capture": {}}]}`,
		`{"version": 1.0, "audiences": [{"id": "a", "match": {}, "capture": {}}]}`,
		`{"version": 1e2, "audiences": [{"id": "a", "match": {}, "capture": {}}]}`,
		`{"version": 1, "audiences": []}`,
		`{"version": 1, "audiences": [{"id": 1, "match": {}, "capture": {}}]}`,
		`{"version": 1, "audiences": [{"id": "a", "match": [], "capture": {}}]}`,
		`{"version": 1, "audiences": [{"id": "a", "match": {}, "capture": []}]}`,
		`{"version": 1, "audiences": ["a"]}`,
	} {
		if parseSettings([]byte(body)) != nil {
			t.Errorf("accepted %.60s", body)
		}
	}
}

func TestTheFirstMatchingAudienceWins(t *testing.T) {
	body := settingsBody(t, 3,
		anAudience("beta", map[string]any{"label": []string{"beta", "alpha"}}, "full"),
		anAudience("claude", map[string]any{"client": []string{"claude-code"}}, "full"),
		everyone)
	for v, want := range map[*matchView]string{
		{labels: []string{"alpha"}, client: "claude-code"}: "beta",
		{client: "claude-code"}:                            "claude",
		{labels: []string{"gamma"}}:                        "everyone",
	} {
		if got := matched(t, body, *v); got != want {
			t.Errorf("%+v matched %q, want %q", *v, got, want)
		}
	}
}

func TestMatchKeysCombineWithAnd(t *testing.T) {
	body := settingsBody(t, 3, anAudience("both", map[string]any{"label": []string{"beta"}, "client": []string{"cursor"}}, "full"), everyone)
	if got := matched(t, body, matchView{labels: []string{"beta"}}); got != "everyone" {
		t.Errorf("one key matched %q", got)
	}
	if got := matched(t, body, matchView{labels: []string{"beta"}, client: "cursor"}); got != "both" {
		t.Errorf("both keys matched %q", got)
	}
}

func TestAnAudienceItCannotReadMatchesNobody(t *testing.T) {
	for _, match := range []string{
		`{"organization": ["org_1"]}`, `{"label": "beta"}`, `{"label": [1]}`, `{"client": "cursor"}`, `{"client": [""]}`,
		`{"share": "0.5"}`, `{"share": true}`, `{"share": 5}`, `{"share": -1}`, `{"share": 0}`,
		`{"share": 1e305}`, `{"share": 1` + strings.Repeat("0", 400) + `}`,
	} {
		body := []byte(`{"version": 3, "audiences": [{"id": "odd", "match": ` + match + `, "capture": {}}, {"id": "everyone", "match": {}, "capture": {}}]}`)
		for _, v := range []matchView{{labels: []string{"beta"}, client: "cursor", shareKey: "u"}, {}} {
			if got := matched(t, body, v); got != "everyone" {
				t.Errorf("%s matched %q for %+v", match, got, v)
			}
		}
	}
}

func TestATinyShareMatchesNobody(t *testing.T) {
	// 0.00001 × 10000 rounds to 0, so no bucket is under it.
	body := settingsBody(t, 3, anAudience("none", map[string]any{"share": 0.00001}, "full"), everyone)
	for i := range 200 {
		if got := matched(t, body, matchView{shareKey: fmt.Sprintf("u%d", i)}); got != "everyone" {
			t.Fatalf("u%d matched %q", i, got)
		}
	}
}

func TestAnyPositiveVersionIsRead(t *testing.T) {
	if d := parseSettings(settingsBody(t, 3000000000, everyone)); d == nil || d.version != 3000000000 {
		t.Error("a version past 2^31 was refused")
	}
}

func TestTheMatchedAudienceSetsResultsAndIsRecorded(t *testing.T) {
	d := parsed(t, settingsBody(t, 7, anAudience("beta", map[string]any{"label": []string{"beta"}}, "full"), everyone))
	beta := d.settingsFor(matchView{labels: []string{"beta"}}, "srv")
	if beta.results != ResultCaptureFull || *beta.capture != (CaptureObserved{SettingsVersion: 7, Audience: "beta"}) {
		t.Errorf("beta: %+v %+v", beta, *beta.capture)
	}
	rest := d.settingsFor(matchView{}, "srv")
	if rest.results != ResultCaptureOff || rest.goal != IntentParamRequired || rest.reports != ReportOff ||
		*rest.capture != (CaptureObserved{SettingsVersion: 7, Audience: "everyone"}) {
		t.Errorf("everyone: %+v %+v", rest, *rest.capture)
	}
}

func TestAnUnknownSettingLeavesTheCodeValue(t *testing.T) {
	for _, results := range []any{"metadata", []string{"full"}, map[string]any{"a": 1}, nil} {
		d := parsed(t, settingsBody(t, 3, anAudience("everyone", map[string]any{}, results)))
		if got := d.settingsFor(matchView{}, "srv").results; got != "" {
			t.Errorf("%v gave %q", results, got)
		}
	}
}

func TestNoMatchingAudienceRecordsTheVersionAlone(t *testing.T) {
	d := parsed(t, settingsBody(t, 3, anAudience("beta", map[string]any{"label": []string{"beta"}}, "full")))
	s := d.settingsFor(matchView{}, "srv")
	if s.results != "" || *s.capture != (CaptureObserved{SettingsVersion: 3}) {
		t.Errorf("%+v %+v", s, *s.capture)
	}
}

func TestReportsAreOnForSomeoneWhileAReachableAudienceHasThem(t *testing.T) {
	on := func(id string, match map[string]any) map[string]any {
		return map[string]any{"id": id, "match": match, "capture": map[string]any{"reports": "on"}}
	}
	for name, tc := range map[string]struct {
		body []byte
		code ReportMode
		want bool
	}{
		"an audience with them":           {settingsBody(t, 1, on("beta", map[string]any{"label": []string{"b"}}), everyone), ReportOn, true},
		"every audience without them":     {settingsBody(t, 1, everyone), ReportOn, false},
		"unset, so the code's value":      {[]byte(`{"version": 1, "audiences": [{"id": "all", "match": {}, "capture": {}}]}`), ReportOn, true},
		"no match for some, code on":      {[]byte(`{"version": 1, "audiences": [{"id": "b", "match": {"label": ["b"]}, "capture": {"reports": "off"}}]}`), ReportOn, true},
		"no match for some, code off":     {[]byte(`{"version": 1, "audiences": [{"id": "b", "match": {"label": ["b"]}, "capture": {"reports": "off"}}]}`), ReportOff, false},
		"everyone matched first, then on": {settingsBody(t, 1, everyone, on("late", map[string]any{})), ReportOn, true},
	} {
		if got := parsed(t, tc.body).reportsOnForAnyone(tc.code); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}
