package host

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// overlayPathFor derives the writable local-overlay path beside a base config:
// kitchen-sink.json -> kitchen-sink.local.json. The overlay holds only the
// mutable picks a user changes at runtime (active connection, approval mode),
// kept separate from the hand-authored base so writing it back never clobbers
// the base file's formatting or unrelated keys. A path with no extension gets
// ".local" appended.
func overlayPathFor(configPath string) string {
	ext := filepath.Ext(configPath)
	return strings.TrimSuffix(configPath, ext) + ".local" + ext
}

// configOverlay persists mutable config picks to a sparse JSON file that is
// merged over the base config on the next load (later wins, via
// LoadConfigWithOverlay). Only the keys a slash command changes are written,
// so the file stays a small delta rather than a full config copy. Safe for
// concurrent use.
type configOverlay struct {
	path string
	mu   sync.Mutex
}

// mutate read-modify-writes the sparse overlay map atomically (temp + rename).
// A missing file starts from an empty map, so the first write creates it; a
// corrupt overlay is overwritten rather than treated as fatal (it only ever
// holds regenerable runtime picks).
func (o *configOverlay) mutate(fn func(map[string]any)) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	m := map[string]any{}
	if raw, err := os.ReadFile(o.path); err == nil {
		_ = json.Unmarshal(raw, &m)
	} else if !os.IsNotExist(err) {
		return err
	}
	fn(m)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	tmp := o.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, o.path)
}

// ensureObj fetches-or-creates a nested JSON object under key, so a write to
// connections.active leaves any sibling keys the overlay already holds intact.
func ensureObj(m map[string]any, key string) map[string]any {
	c, _ := m[key].(map[string]any)
	if c == nil {
		c = map[string]any{}
		m[key] = c
	}
	return c
}

// setActiveConnection persists {"connections":{"active":name}}.
func (o *configOverlay) setActiveConnection(name string) error {
	return o.mutate(func(m map[string]any) { ensureObj(m, "connections")["active"] = name })
}

// setApprovalMode persists {"approval":{"mode":mode}}.
func (o *configOverlay) setApprovalMode(mode string) error {
	return o.mutate(func(m map[string]any) { ensureObj(m, "approval")["mode"] = mode })
}

// persistOverlay applies save to the config overlay when one is configured,
// degrading a write failure to a warning event rather than failing the command
// the user just ran (mirrors the RunStore persistence contract). A no-op when
// WithConfigOverlay was not set.
func (a *App) persistOverlay(what string, save func(*configOverlay) error) {
	if a.overlay == nil {
		return
	}
	if err := save(a.overlay); err != nil {
		a.emit(HostEvent{Kind: HostSessionWarn, Err: fmt.Sprintf("could not persist %s to %s: %v", what, a.overlay.path, err)})
	}
}
