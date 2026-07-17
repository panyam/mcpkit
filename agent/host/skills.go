package host

import (
	"context"
	"fmt"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	skills "github.com/panyam/mcpkit/ext/skills"
)

// loadSkillsBlock fetches and digest-verifies a server's skills and renders
// the instructions section. Servers without the skills capability return the
// empty string silently; verified failures (digest mismatch, unreadable
// skill) are warned to the transcript and excluded, never injected. An index
// that cannot be fetched at all is a startup error: the server advertised
// skills and the host could not honor them, which the user should see before
// conversing, not during.
func loadSkillsBlock(c *client.Client, serverID string, emit func(HostEvent), tp core.TracerProvider) (string, error) {
	sc := skills.NewClient(c, skills.WithTracerProvider(tp))
	if !sc.SupportsSkills() {
		return "", nil
	}
	loaded, err := sc.LoadAll(context.Background())
	if err != nil {
		return "", fmt.Errorf("agentchat: skills index from %s: %w", serverID, err)
	}
	var ok int
	for _, ls := range loaded {
		if ls.Err != nil {
			emit(HostEvent{Kind: HostSkillSkipped, ServerID: serverID, URI: ls.Entry.URL, Err: ls.Err.Error()})
			continue
		}
		ok++
	}
	if ok > 0 || len(loaded) > 0 {
		emit(HostEvent{Kind: HostSkillsLoaded, ServerID: serverID, Loaded: ok, Skipped: len(loaded) - ok})
	}
	return skills.InstructionsBlock(loaded), nil
}
