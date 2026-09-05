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

// driveSEP2640Verify loads a skill and then reads a supporting file.
//
// The scenarios behind this serve a tampered SKILL.md and grade whether the
// client noticed. Detection is by absence: a client that verifies aborts on
// the load and never reaches the supporting file, so completing the second
// step is the violation. Errors are returned rather than swallowed, since the
// scenario sets allowClientError and a non-zero exit here IS the pass.
func driveSEP2640Verify(serverURL, mode string) error {
	c := client.NewClient(serverURL,
		core.ClientInfo{Name: "mcpkit-testclient", Version: "0.1.0"},
		client.WithElicitationHandler(conformanceElicitationHandler),
	)
	if err := c.Connect(context.Background()); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer c.Close()

	sc := skills.NewClient(c)
	entries, err := sc.ListSkillEntries(context.Background())
	if err != nil {
		return fmt.Errorf("skills/list: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("listing was empty, nothing to verify against")
	}
	e := entries[0]

	if mode == "unlisted" {
		// Ask for a file the manifest does not contain. ReadFromEntry must
		// refuse locally, without putting the read on the wire.
		unlisted := "skill://pdf-processing/scripts/extract.py"
		if _, err := sc.ReadFromEntry(context.Background(), e, unlisted); err != nil {
			return fmt.Errorf("correctly refused unlisted read: %w", err)
		}
		return fmt.Errorf("read an unlisted URI without error, which the SEP forbids")
	}

	if _, err := sc.ReadFromEntry(context.Background(), e, e.URI); err != nil {
		return fmt.Errorf("correctly rejected tampered SKILL.md: %w", err)
	}

	// Only reachable if the tampered SKILL.md was accepted. Reaching the
	// supporting file is what the scenario records as the violation.
	if _, err := sc.ReadFromEntry(context.Background(), e, "skill://pdf-processing/references/FORMS.md"); err != nil {
		return fmt.Errorf("supporting read failed: %w", err)
	}
	return fmt.Errorf("accepted a SKILL.md that fails %s verification", mode)
}
