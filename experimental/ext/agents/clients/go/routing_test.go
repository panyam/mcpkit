package agentsclient_test

import (
	"testing"

	"github.com/panyam/mcpkit/experimental/ext/agents"
	agentsclient "github.com/panyam/mcpkit/experimental/ext/agents/clients/go"
	"github.com/stretchr/testify/assert"
)

func TestRoleTableResolve(t *testing.T) {
	small, medium, large := agents.ModelRoleSmall, agents.ModelRoleMedium, agents.ModelRoleLarge
	full := agentsclient.RoleTable[string]{
		Roles: map[agents.ModelRole]agentsclient.RoleModel[string]{
			small:  {Model: "haiku"},
			medium: {Model: "sonnet"},
			large:  {Model: "opus", Vision: true},
		},
		Default: "supervisor",
	}
	noMedium := full
	noMedium.Roles = map[agents.ModelRole]agentsclient.RoleModel[string]{small: full.Roles[small], large: full.Roles[large]}
	noVision := full
	noVision.Roles = map[agents.ModelRole]agentsclient.RoleModel[string]{small: full.Roles[small], medium: full.Roles[medium]}

	cases := []struct {
		name  string
		table agentsclient.RoleTable[string]
		hint  *agents.ModelRoutingHint
		model string
		how   agentsclient.Resolution
	}{
		{"nil hint", full, nil, "supervisor", agentsclient.ResolvedDefault},
		{"exact role", full, &agents.ModelRoutingHint{Role: medium}, "sonnet", agentsclient.ResolvedHinted},
		{"missing role escalates", noMedium, &agents.ModelRoutingHint{Role: medium}, "opus", agentsclient.ResolvedEscalated},
		{"vision escalates", full, &agents.ModelRoutingHint{Role: small, Vision: true}, "opus", agentsclient.ResolvedEscalated},
		{"never downgrades", noVision, &agents.ModelRoutingHint{Role: large}, "supervisor", agentsclient.ResolvedDefault},
		{"vision unsatisfiable", noVision, &agents.ModelRoutingHint{Role: small, Vision: true}, "supervisor", agentsclient.ResolvedDefault},
		{"no role starts small", full, &agents.ModelRoutingHint{}, "haiku", agentsclient.ResolvedEscalated},
		{"no role with vision", full, &agents.ModelRoutingHint{Vision: true}, "opus", agentsclient.ResolvedEscalated},
		{"unknown role", full, &agents.ModelRoutingHint{Role: "huge"}, "supervisor", agentsclient.ResolvedDefault},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			model, how := c.table.Resolve(c.hint)
			assert.Equal(t, c.model, model)
			assert.Equal(t, c.how, how, "got %s, want %s", how, c.how)
		})
	}
}

func TestLargestRole(t *testing.T) {
	assert.Nil(t, agentsclient.LargestRole())
	assert.Nil(t, agentsclient.LargestRole(nil, nil))
	assert.Equal(t,
		&agents.ModelRoutingHint{Role: agents.ModelRoleLarge, Vision: true},
		agentsclient.LargestRole(
			&agents.ModelRoutingHint{Role: agents.ModelRoleMedium, Vision: true},
			nil,
			&agents.ModelRoutingHint{Role: agents.ModelRoleLarge},
			&agents.ModelRoutingHint{Role: agents.ModelRoleSmall},
		))
	assert.Equal(t,
		&agents.ModelRoutingHint{Vision: false},
		agentsclient.LargestRole(&agents.ModelRoutingHint{}),
		"hints with no role merge to no role")
}
