package baton

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const sentSurfacesCap = 256

// The hash covers the vendor's own surface only, never Baton's
// additions, so changing a Baton setting does not look like a new surface.
func (b *installed) maybeSnapshot(l listing) {
	tools := l.tools
	if b.lastSurface.unchanged(tools) {
		return
	}
	toolMaps := sortedToolMaps(tools)
	canonical, err := json.Marshal(map[string]any{
		"server_info":  b.server.serverInfo,
		"capabilities": b.server.capabilities,
		"instructions": nonEmpty(b.server.instructions),
		"tools":        toolMaps,
	})
	if err != nil {
		return
	}
	sum := sha256.Sum256(canonical)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	isNew := false
	b.sentSurfaces.with(digest, func(_ struct{}, found bool) struct{} {
		isNew = !found
		return struct{}{}
	})
	if !isNew {
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
		ServerInfo:   b.server.serverInfo,
		Capabilities: b.server.capabilities,
		Instructions: nonEmpty(b.server.instructions),
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
