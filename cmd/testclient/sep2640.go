package main

// SEP-2640 skills client drivers.
//
// These scenarios invert the usual direction: the harness stands up the
// server and grades what mcpkit's client does, so each driver's job is to
// perform exactly the interaction the scenario contract names and nothing
// more. An extra request is a failure, not noise.
//
// Every URI is derived from the listing the server sent. The drivers know no
// fixture path, matching the server-side scenarios, which hardcode no fixture
// URI either. A driver that named the harness's own files would break on a
// fixture rename and, worse, would still pass while doing so.
//
// All verification lives in ext/skills. These drivers check nothing
// themselves, so what the suite grades is mcpkit's client rather than a
// stand-in written to satisfy it.

import (
	"context"
	"fmt"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/skills"
)

// connectSkills dials the scenario's server and enumerates once, which every
// driver needs and no driver should do differently.
func connectSkills(serverURL string) (*client.Client, *skills.Client, []skills.SkillEntry, error) {
	c := client.NewClient(serverURL,
		core.ClientInfo{Name: "mcpkit-testclient", Version: "0.1.0"},
		client.WithElicitationHandler(conformanceElicitationHandler),
	)
	if err := c.Connect(context.Background()); err != nil {
		return nil, nil, nil, fmt.Errorf("connect: %w", err)
	}

	sc := skills.NewClient(c)
	if !sc.SupportsSkills() {
		c.Close()
		return nil, nil, nil, fmt.Errorf("server did not declare the skills extension")
	}

	entries, err := sc.ListSkillEntries(context.Background())
	if err != nil {
		c.Close()
		return nil, nil, nil, fmt.Errorf("skills/list: %w", err)
	}
	return c, sc, entries, nil
}

// supportingURI returns a file the entry lists that is not the manifest. This
// is the file a client only reaches by having accepted the SKILL.md, so the
// scenarios use a read of it as proof that verification did not happen.
func supportingURI(e skills.SkillEntry) (string, bool) {
	for _, r := range e.Resources.Files {
		if r.URI != e.URI {
			return r.URI, true
		}
	}
	return "", false
}

// driveSEP2640NoPrefetch connects, enumerates, and stops.
//
// SEP-2640 makes lazy retrieval a MUST NOT: a host may not fetch a skill's
// files on connection, on listing, or at approval. The scenario serves a
// listing whose entries name a SKILL.md and a supporting file, then fails the
// run if either is read. So the contract here is literally to enumerate and
// leave: any resources/read before a deliberate load is the violation.
func driveSEP2640NoPrefetch(serverURL string) error {
	c, _, entries, err := connectSkills(serverURL)
	if err != nil {
		return err
	}
	defer c.Close()

	// Touch only the metadata the listing already carried. Reading a body
	// here is exactly what the scenario is watching for.
	for _, e := range entries {
		if e.Name() == "" {
			return fmt.Errorf("entry %s carries no frontmatter name", e.URI)
		}
	}
	return nil
}

// driveSEP2640Verify exercises the read-time verification MUSTs.
//
// One behaviour across all three modes: the server has tampered with the
// SKILL.md in a mode-specific way, and the client's job is identical in each
// case, which is to reject it and never reach the supporting file. The
// manifest read is not incidental, it is the scenario's prerequisite: without
// it an empty read log proves nothing and the check reports untestable.
//
// A fourth mode, `unlisted`, was retired from the suite on 2026-09-10.
//
// Errors are returned rather than swallowed, since the scenario sets
// allowClientError and a non-zero exit here IS the pass.
func driveSEP2640Verify(serverURL, mode string) error {
	c, sc, entries, err := connectSkills(serverURL)
	if err != nil {
		return err
	}
	defer c.Close()

	if len(entries) == 0 {
		return fmt.Errorf("listing was empty, nothing to verify against")
	}
	e := entries[0]

	if _, err := sc.ReadFromEntry(context.Background(), e, e.URI); err != nil {
		return fmt.Errorf("correctly rejected tampered SKILL.md: %w", err)
	}

	// Only reachable if the tampered SKILL.md was accepted. Reaching the
	// supporting file is what the scenario records as the violation, so a
	// listing with nothing but the manifest would leave the run unable to
	// demonstrate it either way.
	supporting, ok := supportingURI(e)
	if !ok {
		return fmt.Errorf("accepted a SKILL.md that fails %s verification (entry lists no supporting file to continue to)", mode)
	}
	if _, err := sc.ReadFromEntry(context.Background(), e, supporting); err != nil {
		return fmt.Errorf("supporting read failed: %w", err)
	}
	return fmt.Errorf("accepted a SKILL.md that fails %s verification", mode)
}
