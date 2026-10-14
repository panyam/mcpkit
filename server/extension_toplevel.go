package server

import (
	"sort"

	"github.com/panyam/mcpkit/core"
)

// extensionTopLevel collects the top-level capability keys registered
// extensions contribute (core.Extension.TopLevel). Extensions are walked in ID
// order and the first to claim a key keeps it, so two extensions declaring the
// same key resolve the same way on every response rather than by map
// iteration (constraint C10). Returns nil when no extension contributes any.
func extensionTopLevel(exts map[string]core.Extension) map[string]any {
	ids := make([]string, 0, len(exts))
	for id, ext := range exts {
		if len(ext.TopLevel) > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	sort.Strings(ids)
	out := map[string]any{}
	for _, id := range ids {
		for key, value := range exts[id].TopLevel {
			if _, taken := out[key]; !taken {
				out[key] = value
			}
		}
	}
	return out
}
