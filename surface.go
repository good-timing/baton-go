package baton

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"reflect"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const sentSurfacesCap = 256

// The hash covers the vendor's own surface only, never Baton's
// additions, so changing a Baton setting does not look like a new surface.
func (b *installed) maybeSnapshot(l listing) {
	caps := b.capabilities.Load()
	if caps == nil {
		b.noCaps.RLock()
		defer b.noCaps.RUnlock()
		caps = b.capabilities.Load()
	}
	if b.lastSurface.unchanged(l, caps) {
		return
	}
	toolMaps := b.sortedToolMaps(l)
	canonical, err := json.Marshal(map[string]any{
		"server_info":  b.serverInfo,
		"capabilities": caps.json(),
		"instructions": nonEmpty(b.instructions),
		"tools":        toolMaps,
	})
	if err != nil {
		return
	}
	sum := sha256.Sum256(canonical)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if !b.sentSurfaces.add(digest) {
		return
	}
	injected := []string{}
	if b.servesAnnotate(l) {
		injected = append(injected, b.annotateName)
	}
	// The surface is the server's, not a caller's, so it goes on the
	// process-wide session, as in baton-sdk.
	b.emit(callContext{session: b.sessions.process()}, "surface_snapshot", surfaceSnapshot{
		SurfaceHash:  digest,
		ServerInfo:   b.serverInfo,
		Capabilities: caps.json(),
		Instructions: nonEmpty(b.instructions),
		Tools:        toolMaps,
		SeamAugmentations: seamAugmentations{
			InjectedTools: injected,
			IntentParam: intentParam{
				Names:         []string{userGoalParam},
				RequiredNames: requiredNames(b.cfg.IntentParamMode),
				Mode:          string(b.cfg.IntentParamMode),
			},
			// The Config's value, not the document's (TestTheSnapshotsInstructionsSuffixIsTheCodes).
			InstructionsSuffix: b.ourInstructions(l, true) != "",
		},
	})
}

type capabilities struct{ value any }

func (c *capabilities) json() any {
	if c == nil {
		return nil
	}
	return c.value
}

// keepSetupAnswer keeps the capabilities of the first initialize or
// server/discover answer, which hold for every server b wraps: they are copies
// of one server. An initialize answer's server name is checked against
// impl.Name, and a difference is warned about once.
func (b *Baton) keepSetupAnswer(res mcp.Result) {
	if init, ok := res.(*mcp.InitializeResult); ok && init != nil && init.ServerInfo != nil && init.ServerInfo.Name != b.serverName {
		b.nameDriftWarned.Do(func() {
			slog.Warn("baton: the server answers with a different name than the impl given to Baton; pass the impl the server was built with",
				"server", init.ServerInfo.Name, "baton", b.serverName)
		})
	}
	if b.capabilities.Load() != nil {
		return
	}
	if f := structField(res, "Capabilities"); f.IsValid() {
		caps := &capabilities{jsonValue(f.Interface())}
		b.noCaps.Lock()
		b.capabilities.CompareAndSwap(nil, caps)
		b.noCaps.Unlock()
	}
}

// jsonValue is v as plain JSON data: maps, slices, strings, and numbers as
// json.Number, so an integer past 2^53 keeps every digit.
func jsonValue(v any) any {
	raw, ok := v.(json.RawMessage)
	if !ok {
		var err error
		if raw, err = json.Marshal(v); err != nil {
			return nil
		}
	}
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil
	}
	return out
}

// resultValue is jsonValue(r) with plain-text content copied directly, so a
// large text result skips the JSON round trip. Every other field still goes
// through go-sdk's own encoding.
func resultValue(r *mcp.CallToolResult) any {
	if r.Content == nil || !textContentIsPlain {
		return jsonValue(r)
	}
	content := make([]any, 0, len(r.Content))
	for _, c := range r.Content {
		t, ok := c.(*mcp.TextContent)
		// json.Marshal rewrites invalid UTF-8, one U+FFFD per bad byte.
		if !ok || len(t.Meta) > 0 || t.Annotations != nil || !utf8.ValidString(t.Text) {
			return jsonValue(r)
		}
		content = append(content, map[string]any{"type": "text", "text": t.Text})
	}
	rest := *r
	rest.Content = nil
	out, ok := jsonValue(&rest).(map[string]any)
	if !ok {
		return jsonValue(r)
	}
	out["content"] = content
	return out
}

// textContentIsPlain holds while mcp.TextContent has only the fields
// resultValue copies; a go-sdk that adds one sends every result the slow way.
var textContentIsPlain = func() bool {
	t := reflect.TypeOf(mcp.TextContent{})
	want := map[string]bool{"Text": true, "Meta": true, "Annotations": true}
	if t.NumField() != len(want) {
		return false
	}
	for i := range t.NumField() {
		if !want[t.Field(i).Name] {
			return false
		}
	}
	return true
}()

func requiredNames(mode IntentParamMode) []string {
	if mode == IntentParamRequired {
		return []string{userGoalParam}
	}
	return []string{}
}
