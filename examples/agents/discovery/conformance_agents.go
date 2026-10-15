package main

import (
	"encoding/json"
	"fmt"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/agents"
	"github.com/panyam/mcpkit/server"
)

// The agents conformance fixture (--conformance-agents), driven by the
// sep-9998-agents-* scenarios via make testconf-agents. It is off by default,
// per examples/CONVENTIONS.md § Conformance fixtures, and everything it
// provokes goes through public agents API.
//
// The harness holds no credential, so it is an anonymous caller. Two rows
// need an agent in a particular state, and the suite asks for their names
// through agents_conformance_fixture rather than hardcoding them:
//
//   - hiddenAgent is billing-agent, which Visible already hides from any
//     caller without billing:read, so this reports configuration rather than
//     adding any.
//   - brokenAgent names a tool the catalog does not have, which is the
//     misconfiguration invalid_agent_definition reports. Register accepts it
//     with an advisory error, as it would for a real author's typo.
//
// A page size of one makes agents/list paginate, so the cursor rows have
// pages to walk.

const (
	conformanceBrokenAgent = "conformance-broken-agent"
	conformanceMissingTool = "conformance_missing_tool"
)

func applyConformance(cfg *agents.Config) {
	cfg.PageSize = 1
	cfg.Agents = append(cfg.Agents, agents.AgentDef{
		Name:         conformanceBrokenAgent,
		Description:  "Names a tool the server does not have",
		Instructions: "Exists so agents/get can report invalid_agent_definition.",
		Tools:        []string{conformanceMissingTool},
	})
}

type addAgentArgs struct {
	Name string `json:"name" jsonschema:"description=name of the agent to add"`
}

func registerConformanceControls(srv *server.Server, reg *agents.Registry) {
	srv.Register(core.TextTool[struct{}]("agents_conformance_fixture",
		"Conformance control: names the agent hidden from this caller and the agent whose membership names a missing tool",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			b, err := json.Marshal(map[string]string{
				"hiddenAgent": "billing-agent",
				"brokenAgent": conformanceBrokenAgent,
			})
			return string(b), err
		}))
	srv.Register(core.TextTool[addAgentArgs]("agents_conformance_add_agent",
		"Conformance control: adds a valid agent with the given name, so the server sends notifications/agents/list_changed",
		func(ctx core.ToolContext, a addAgentArgs) (string, error) {
			if a.Name == "" {
				return "", fmt.Errorf("name is required")
			}
			if err := reg.AddAgent(agents.AgentDef{
				Name:        a.Name,
				Description: "Added by the conformance harness",
				Tools:       []string{"list_failed_pipelines"},
			}); err != nil {
				return "", err
			}
			return "added " + a.Name, nil
		}))
}
