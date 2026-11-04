package baton

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestUsableLabelsAreKeptInOrderWithoutRepeats(t *testing.T) {
	if got := cleanLabels([]string{"beta", "org_1", "beta", "alpha"}); !slices.Equal(got, []string{"beta", "org_1", "alpha"}) {
		t.Errorf("got %q", got)
	}
}

func TestAnUnusableLabelIsDroppedAlone(t *testing.T) {
	for _, bad := range []string{"", " \t　", strings.Repeat("x", 65), "a\x00b", "\xff"} {
		if got := cleanLabels([]string{"a", bad, "b"}); !slices.Equal(got, []string{"a", "b"}) {
			t.Errorf("%q: got %q", bad, got)
		}
	}
	if got := cleanLabels([]string{strings.Repeat("é", 64)}); len(got) != 1 {
		t.Error("a label of 64 characters was dropped")
	}
	if got := cleanLabels([]string{"", " "}); got != nil {
		t.Errorf("no usable labels gave %#v, want nil", got)
	}
}

func TestAtMostTwentyLabelsAreKept(t *testing.T) {
	var given []string
	for i := range 25 {
		given = append(given, strings.Repeat("l", i+1))
	}
	if got := cleanLabels(given); !slices.Equal(got, given[:20]) {
		t.Errorf("got %d labels", len(got))
	}
}

func labelsSent(t *testing.T, p *Principal) []Event {
	t.Helper()
	server, sink := installWithSink(t, Config{
		ResolvePrincipal: func(context.Context, Caller) (*Principal, error) { return p, nil },
	}, addLookup)
	call(t, connect(t, server), "lookup", map[string]any{"name": "a"})
	return ofType(sink.all(), "tool_call_start")
}

func TestTheCallersLabelsRideTheEvent(t *testing.T) {
	for name, tc := range map[string]struct {
		p    *Principal
		want []string
	}{
		"principal then organization": {&Principal{ID: "u", Labels: []string{"beta", "x"}, Organization: &Organization{ID: "o", Labels: []string{"x", "enterprise"}}}, []string{"beta", "x", "enterprise"}},
		"an organization alone":       {&Principal{Organization: &Organization{ID: "o", Labels: []string{"enterprise"}}}, []string{"enterprise"}},
		"labels alone":                {&Principal{Labels: []string{"beta"}}, []string{"beta"}},
		"not scrubbed":                {&Principal{ID: "u", Labels: []string{"a@b.co"}}, []string{"a@b.co"}},
	} {
		ev := labelsSent(t, tc.p)[0]
		if !slices.Equal(ev.Labels, tc.want) {
			t.Errorf("%s: labels %q, want %q", name, ev.Labels, tc.want)
		}
	}
}

func TestNoLabelsAndNoCaptureAreSentAsNull(t *testing.T) {
	ev := labelsSent(t, &Principal{ID: "u", Labels: []string{" "}})[0]
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"labels", "capture"} {
		if v, ok := envelope[key]; !ok || string(v) != "null" {
			t.Errorf("%s is %s, want null", key, v)
		}
	}
}
