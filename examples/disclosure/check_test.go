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
// each wire. Every row must pass, and the size row must be present.
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
			for _, name := range []string{rowPlainTools, rowPlainSkill, rowGroupsList, rowGroupsGet, rowGroupsTools} {
				if r, ok := got[name]; !ok || !r.OK {
					t.Errorf("%s: %+v", name, r)
				}
			}
			if _, ok := got[rowSizes]; !ok {
				t.Errorf("row %q missing from the report", rowSizes)
			}
		})
	}
}

func TestFixtureServesTwoHundredTools(t *testing.T) {
	if toolCount != 200 || len(fixtureTools()) != toolCount {
		t.Fatalf("toolCount=%d, fixtureTools=%d, want 200", toolCount, len(fixtureTools()))
	}
}
