package baton

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"slices"
)

// The capture settings document the Console serves (SPEC §2.6): read, then
// matched to each caller.

// callSettings is what one caller runs under. An empty mode means the
// Config's value.
type callSettings struct {
	capture *CaptureObserved
	results ResultCaptureMode
	goal    IntentParamMode
	reports ReportMode
}

// audience is one audience of a document, its match read once.
type audience struct {
	settings callSettings
	// A nil set is no rule of that kind.
	labels, clients map[string]bool
	shares          bool
	shareThreshold  int
}

func (a audience) matchesEveryone() bool {
	return a.labels == nil && a.clients == nil && !a.shares
}

// matchView is what a match rule can see of one request.
type matchView struct {
	labels   []string
	client   string
	shareKey string
}

func (a audience) matches(v matchView, bucket int, bucketed bool) bool {
	if a.labels != nil && !slices.ContainsFunc(v.labels, func(l string) bool { return a.labels[l] }) {
		return false
	}
	if a.clients != nil && (v.client == "" || !a.clients[v.client]) {
		return false
	}
	if a.shares && (!bucketed || bucket >= a.shareThreshold) {
		return false
	}
	return true
}

type settingsDocument struct {
	version   int
	audiences []audience
	unmatched callSettings
	usesShare bool
}

func (d *settingsDocument) settingsFor(v matchView, vendorID string) callSettings {
	bucketed := d.usesShare && v.shareKey != ""
	bucket := 0
	if bucketed {
		bucket = shareBucket(vendorID, v.shareKey)
	}
	for _, a := range d.audiences {
		if a.matches(v, bucket, bucketed) {
			return a.settings
		}
	}
	return d.unmatched
}

// reportsOnForAnyone is whether some caller can get reports, so the
// instructions point to the report tool.
func (d *settingsDocument) reportsOnForAnyone(code ReportMode) bool {
	reachable := make([]callSettings, 0, len(d.audiences)+1)
	for _, a := range d.audiences {
		reachable = append(reachable, a.settings)
	}
	if !slices.ContainsFunc(d.audiences, audience.matchesEveryone) {
		reachable = append(reachable, d.unmatched)
	}
	return slices.ContainsFunc(reachable, func(s callSettings) bool { return cmp.Or(s.reports, code) == ReportOn })
}

// shareBucket is SPEC §2.6's bucket: the first 8 bytes of SHA-256 over
// vendorID, a line feed and key, big-endian, modulo 10000.
func shareBucket(vendorID, key string) int {
	sum := sha256.Sum256([]byte(vendorID + "\n" + key))
	return int(binary.BigEndian.Uint64(sum[:8]) % 10000)
}

// parseSettings is the document in body, or nil when it is not a valid one
// (SPEC §2.6).
func parseSettings(body []byte) *settingsDocument {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var data map[string]any
	if dec.Decode(&data) != nil || data == nil || dec.Decode(&struct{}{}) != io.EOF {
		return nil
	}
	version, ok := positiveInt(data["version"])
	if !ok {
		return nil
	}
	list, _ := data["audiences"].([]any)
	if len(list) == 0 {
		return nil
	}
	d := &settingsDocument{version: version, unmatched: callSettings{capture: &CaptureObserved{SettingsVersion: version}}}
	for _, item := range list {
		a, _ := item.(map[string]any)
		id, idOK := a["id"].(string)
		match, matchOK := a["match"].(map[string]any)
		capture, captureOK := a["capture"].(map[string]any)
		if !idOK || !matchOK || !captureOK {
			return nil
		}
		if readable, ok := readAudience(version, id, match, capture); ok {
			d.audiences = append(d.audiences, readable)
			d.usesShare = d.usesShare || readable.shares
		}
	}
	return d
}

// positiveInt is v as an integer of at least 1. A JSON number with a
// fraction or an exponent is not one, as in baton-sdk.
func positiveInt(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	if err != nil || i < 1 || i > math.MaxInt32 {
		return 0, false
	}
	return int(i), true
}

// readAudience is false for an audience that can match nobody (SPEC §2.6).
func readAudience(version int, id string, match, capture map[string]any) (audience, bool) {
	var a audience
	for key, value := range match {
		var ok bool
		switch key {
		case "label":
			a.labels, ok = stringSet(value)
		case "client":
			a.clients, ok = stringSet(value)
		case "share":
			a.shareThreshold, ok = shareThreshold(value)
			a.shares = true
		}
		if !ok {
			return audience{}, false
		}
	}
	a.settings = callSettings{
		capture: &CaptureObserved{SettingsVersion: version, Audience: id},
		results: knownMode(capture["results"], ResultCaptureFull, ResultCaptureOff),
		goal:    knownMode(capture["goal"], IntentParamRequired, IntentParamOptional),
		reports: knownMode(capture["reports"], ReportOn, ReportOff),
	}
	return a, true
}

func stringSet(v any) (map[string]bool, bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	set := make(map[string]bool, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		set[s] = true
	}
	return set, true
}

// shareThreshold is share × 10000 with a half rounding up, for a share above
// 0 and at most 1.
func shareThreshold(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	share, err := n.Float64()
	if err != nil || !(share > 0 && share <= 1) {
		return 0, false
	}
	return int(math.Floor(share*10000 + 0.5)), true
}

// knownMode is v when it is one of the mode's values, else "" for the
// Config's value.
func knownMode[M ~string](v any, values ...M) M {
	s, _ := v.(string)
	if slices.Contains(values, M(s)) {
		return M(s)
	}
	return ""
}
