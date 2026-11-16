package main

// SEP-2640 drivers for the hosting scenarios: load by URI, invalid entries,
// directory children the entry does not list, and two servers at once.
//
// Each contract tells the driver to try something, and ext/skills is what
// must refuse it. So these drivers attempt every load the contract names and
// log the refusals rather than filtering anything themselves. A driver that
// skipped the unlisted child on its own would pass while grading nothing.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/panyam/mcpkit/ext/skills"
)

const directoryMime = "inode/directory"

// sep2640HostingDrivers maps each hosting scenario to its driver. Every driver
// takes the server URL and the raw MCP_CONFORMANCE_CONTEXT.
var sep2640HostingDrivers = map[string]func(serverURL, contextJSON string) error{
	"sep-2640-client-load-by-uri":              driveSEP2640LoadByURI,
	"sep-2640-client-invalid-entry":            func(u, _ string) error { return driveSEP2640InvalidEntry(u) },
	"sep-2640-client-directory-unlisted-child": func(u, _ string) error { return driveSEP2640DirectoryUnlistedChild(u) },
	"sep-2640-client-two-origins":              driveSEP2640TwoOrigins,
}

// hostingContext is what the hosting scenarios put in MCP_CONFORMANCE_CONTEXT.
type hostingContext struct {
	SkillURI        string `json:"skillUri"`
	SecondServerURL string `json:"secondServerUrl"`
}

func parseHostingContext(raw string) (hostingContext, error) {
	var hc hostingContext
	if raw == "" {
		return hc, fmt.Errorf("MCP_CONFORMANCE_CONTEXT is empty")
	}
	if err := json.Unmarshal([]byte(raw), &hc); err != nil {
		return hc, fmt.Errorf("MCP_CONFORMANCE_CONTEXT: %w", err)
	}
	return hc, nil
}

// loadEntry reads every file the entry lists, SKILL.md first, through the
// entry, and stops at the first refusal.
func loadEntry(sc *skills.Client, e skills.SkillEntry) error {
	if _, err := sc.ReadFromEntry(context.Background(), e, e.URI); err != nil {
		return err
	}
	for _, f := range e.Resources.Files {
		if f.URI == e.URI {
			continue
		}
		if _, err := sc.ReadFromEntry(context.Background(), e, f.URI); err != nil {
			return err
		}
	}
	return nil
}

// driveSEP2640LoadByURI lists (the scenario answers an empty listing), then
// loads the skill the context names by its URI alone.
func driveSEP2640LoadByURI(serverURL, contextJSON string) error {
	hc, err := parseHostingContext(contextJSON)
	if err != nil {
		return err
	}
	c, sc, _, err := connectSkills(serverURL)
	if err != nil {
		return err
	}
	defer c.Close()
	e, err := sc.GetSkill(context.Background(), hc.SkillURI)
	if err != nil {
		return err
	}
	_, err = sc.ReadFromEntry(context.Background(), e, e.URI)
	return err
}

// driveSEP2640InvalidEntry tries to load every listed entry. ext/skills must
// refuse the invalid ones before any read.
func driveSEP2640InvalidEntry(serverURL string) error {
	c, sc, entries, err := connectSkills(serverURL)
	if err != nil {
		return err
	}
	defer c.Close()
	for _, e := range entries {
		if err := loadEntry(sc, e); err != nil {
			log.Printf("refused %s: %v", e.URI, err)
		}
	}
	return nil
}

// driveSEP2640DirectoryUnlistedChild loads the first skill, walks its
// directory from the root, and reads every file the directory lists through
// the held entry. ext/skills must refuse a child the entry does not list.
func driveSEP2640DirectoryUnlistedChild(serverURL string) error {
	c, sc, entries, err := connectSkills(serverURL)
	if err != nil {
		return err
	}
	defer c.Close()
	if len(entries) == 0 {
		return fmt.Errorf("listing was empty, no skill to walk")
	}
	e := entries[0]
	if _, err := sc.ReadFromEntry(context.Background(), e, e.URI); err != nil {
		return err
	}
	queue := []string{strings.TrimSuffix(e.URI, "/SKILL.md")}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		children, err := sc.ReadDirectoryAll(context.Background(), dir)
		if err != nil {
			return fmt.Errorf("resources/directory/read %s: %w", dir, err)
		}
		for _, child := range children {
			if child.MimeType == directoryMime {
				queue = append(queue, child.URI)
				continue
			}
			if child.URI == e.URI {
				continue
			}
			if _, err := sc.ReadFromEntry(context.Background(), e, child.URI); err != nil {
				log.Printf("refused %s: %v", child.URI, err)
			}
		}
	}
	return nil
}

// driveSEP2640TwoOrigins connects to both servers and loads every skill each
// one lists, from the server that listed it.
func driveSEP2640TwoOrigins(serverURL, contextJSON string) error {
	hc, err := parseHostingContext(contextJSON)
	if err != nil {
		return err
	}
	for _, url := range []string{serverURL, hc.SecondServerURL} {
		c, sc, entries, err := connectSkills(url)
		if err != nil {
			return fmt.Errorf("%s: %w", url, err)
		}
		for _, e := range entries {
			if err := loadEntry(sc, e); err != nil {
				c.Close()
				return fmt.Errorf("%s: load %s: %w", url, e.URI, err)
			}
		}
		c.Close()
	}
	return nil
}
