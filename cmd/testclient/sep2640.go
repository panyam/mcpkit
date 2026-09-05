package main

// SEP-2640 skills client drivers.
//
// These scenarios invert the usual direction: the harness stands up the
// server and grades what mcpkit's client does, so each driver's job is to
// perform exactly the interaction the scenario contract names and nothing
// more. An extra request is a failure, not noise.

import (
	"context"
	"fmt"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
)

// driveSEP2640NoPrefetch connects, enumerates, and stops.
//
// SEP-2640 makes lazy retrieval a MUST NOT: a host may not fetch a skill's
// files on connection, on listing, or at approval. The scenario serves a
// listing whose entries name a SKILL.md and a supporting file, then fails the
// run if either is read. So the contract here is literally to enumerate and
// leave: any resources/read before a deliberate load is the violation.
func driveSEP2640NoPrefetch(serverURL string) error {
	c := client.NewClient(serverURL,
		core.ClientInfo{Name: "mcpkit-testclient", Version: "0.1.0"},
		client.WithElicitationHandler(conformanceElicitationHandler),
	)
	if err := c.Connect(context.Background()); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer c.Close()

	sc := skills.NewClient(c)
	if !sc.SupportsSkills() {
		return fmt.Errorf("server did not declare the skills extension")
	}

	entries, err := sc.ListSkillEntries(context.Background())
	if err != nil {
		return fmt.Errorf("skills/list: %w", err)
	}

	// Touch only the metadata the listing already carried. Reading a body
	// here is exactly what the scenario is watching for.
	for _, e := range entries {
		if e.Name() == "" {
			return fmt.Errorf("entry %s carries no frontmatter name", e.URI)
		}
	}
	return nil
}
