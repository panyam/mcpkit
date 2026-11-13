package main

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
	"github.com/panyam/mcpkit/server/stateless"
)

// TestChecksOnBothWires runs the exercise's checks against the fixture on
// each wire. The plain-client rows must pass. The groups rows only have to be
// present: they fail until the groups extension exists, and the exercise
// reports them rather than this test.
func TestChecksOnBothWires(t *testing.T) {
	wires := []struct {
		name   string
		server stateless.Mode
		client client.ClientMode
	}{
		{"legacy", stateless.ModeLegacyOnly, client.ClientModeLegacyOnly},
		{"stateless", stateless.ModeStateless, client.ClientModeStateless},
	}
	for _, w := range wires {
		t.Run(w.name, func(t *testing.T) {
			srv := server.NewServer(core.ServerInfo{Name: "disclosure", Version: "0.1.0"})
			if err := registerFixture(srv); err != nil {
				t.Fatal(err)
			}
			ts := httptest.NewServer(srv.Handler(server.WithStreamableHTTP(true), server.WithStatelessMode(w.server)))
			defer ts.Close()

			rows := runChecks(context.Background(), ts.URL+"/mcp", client.WithClientMode(w.client))
			got := map[string]checkRow{}
			for _, r := range rows {
				got[r.Name] = r
			}
			for _, name := range []string{rowPlainTools, rowPlainSkill} {
				if r, ok := got[name]; !ok || !r.OK {
					t.Errorf("%s: %+v", name, r)
				}
			}
			for _, name := range []string{rowGroupsList, rowGroupsGet, rowGroupsTools, rowSizes} {
				if _, ok := got[name]; !ok {
					t.Errorf("row %q missing from the report", name)
				}
			}
		})
	}
}

func TestFixtureServesTwoHundredTools(t *testing.T) {
	if toolCount != 200 || len(fixtureTools()) != toolCount {
		t.Fatalf("toolCount=%d, fixtureTools=%d, want 200", toolCount, len(fixtureTools()))
	}
}
