package baton

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"

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
				RequiredNames: b.requiredNames,
				Mode:          b.cfg.IntentParam,
			},
			InstructionsSuffix: b.ourInstructions(l) != "",
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
