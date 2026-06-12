// Phase 4 — replica-rotation list. This is the ONE in-binary MCP step
// the walkthrough makes; every other step is pure prose. The closure
// returned by runListRotation is wired into demo.Step.Run() inside
// walkthrough.go.
//
// Default config picks alice/asgard so the operator can fire the step
// without env-var setup; override via TENANT/USERNAME/PASSWORD/etc.
// envs (same pattern as the streamer/poller binaries — realm name
// derives from --tenant, password defaults to username because the demo
// realm seeds align by convention).
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/panyam/demokit"
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

// listRotationCalls is how many sequential events/list calls the demo
// fires. Six is enough for a sample size that almost certainly visits
// every replica under nginx's default round-robin policy (N=3), without
// blowing the operator's attention budget. The step also caps wall time
// at listRotationCallSpacing × listRotationCalls.
const (
	listRotationCalls        = 6
	listRotationCallSpacing  = 200 * time.Millisecond
	defaultListRotationRealm = "asgard"
	defaultListRotationUser  = "alice"
)

// listResult is the events/list response wire shape — just enough to
// count entries. The walkthrough doesn't need the full EventDef bag.
type listResult struct {
	Events []json.RawMessage `json:"events"`
}

// runListRotation builds the closure wired into the demo's Phase 4
// step. Captures serverURL so the step can reach the nginx frontdoor
// even when the operator overrode it via --url. Token acquisition is
// LAZY — happens inside the returned closure, not at runDemo construction
// time, so the prose-only steps before Phase 4 never need a token and
// never call Keycloak.
func runListRotation(serverURL string) func(demokit.StepContext) *demokit.StepResult {
	return func(ctx demokit.StepContext) *demokit.StepResult {
		tenant := envOr("TENANT", "")
		realm := tenantRealm(tenant)
		if realm == "" {
			realm = defaultListRotationRealm
		}
		username := envOr("USERNAME", defaultListRotationUser)
		password := envOr("PASSWORD", username)
		keycloakURL := envOr("KEYCLOAK_URL", "http://localhost:8180")
		clientID := envOr("OAUTH_CLIENT_ID", "mcp-events-poller")
		clientSecret := envOr("OAUTH_CLIENT_SECRET", "mcpkit-demo-secret-DEMO-ONLY")

		token, err := acquireDemoToken(keycloakURL, realm, clientID, clientSecret, username, password)
		if err != nil {
			return &demokit.StepResult{
				Status:  demokit.StatusError,
				Message: fmt.Sprintf("ROPC token acquisition failed for realm=%s user=%s: %v", realm, username, err),
				Err:     err,
			}
		}

		var lastReplica atomic.Value
		lastReplica.Store("")

		// Per-call X-Replica capture. The hook fires on every HTTP
		// response from the transport; we stash the value into a slot
		// indexed by call number so the caller can correlate.
		type sample struct {
			replica string
			events  int
		}
		samples := make([]sample, 0, listRotationCalls)
		var pendingReplica atomic.Value
		pendingReplica.Store("")

		c := client.NewClient(serverURL+"/mcp", core.ClientInfo{
			Name:    "whole-enchilada-walkthrough",
			Version: "0.1.0",
		},
			client.WithClientBearerToken(token),
			client.WithClientMode(client.ClientModeStateless),
			client.WithInspectResponse(func(resp *http.Response) {
				r := resp.Header.Get("X-Replica")
				if r == "" {
					return
				}
				pendingReplica.Store(r)
				prev, _ := lastReplica.Load().(string)
				if r != prev {
					lastReplica.Store(r)
				}
			}),
		)
		if err := c.Connect(); err != nil {
			return &demokit.StepResult{
				Status:  demokit.StatusError,
				Message: fmt.Sprintf("Connect to %s failed: %v", serverURL, err),
				Err:     err,
			}
		}
		defer func() { _ = c.Close() }()

		seen := map[string]int{}
		var eventCount int
		for i := 1; i <= listRotationCalls; i++ {
			if ctx.Ctx != nil {
				select {
				case <-ctx.Ctx.Done():
					return &demokit.StepResult{
						Status:  demokit.StatusWarning,
						Message: fmt.Sprintf("Cancelled after %d/%d calls", i-1, listRotationCalls),
					}
				default:
				}
			}
			pendingReplica.Store("")
			res, err := c.Call("events/list", map[string]any{})
			if err != nil {
				return &demokit.StepResult{
					Status:  demokit.StatusError,
					Message: fmt.Sprintf("events/list call %d/%d failed: %v", i, listRotationCalls, err),
					Err:     err,
				}
			}
			replica, _ := pendingReplica.Load().(string)
			if replica == "" {
				replica = "?"
			}
			seen[replica]++

			var parsed listResult
			if err := json.Unmarshal(res.Raw, &parsed); err != nil {
				return &demokit.StepResult{
					Status:  demokit.StatusError,
					Message: fmt.Sprintf("events/list response decode failed (call %d): %v", i, err),
					Err:     err,
				}
			}
			if i == 1 {
				eventCount = len(parsed.Events)
			} else if len(parsed.Events) != eventCount {
				return &demokit.StepResult{
					Status:  demokit.StatusError,
					Message: fmt.Sprintf("events/list returned %d entries on call %d; expected %d (matches call 1)", len(parsed.Events), i, eventCount),
				}
			}
			samples = append(samples, sample{replica: replica, events: len(parsed.Events)})
			fmt.Printf("  call %d: served by %s — events=%d\n", i, replica, len(parsed.Events))

			if i < listRotationCalls {
				time.Sleep(listRotationCallSpacing)
			}
		}

		// Summary line — what we proved.
		uniques := make([]string, 0, len(seen))
		for k := range seen {
			uniques = append(uniques, fmt.Sprintf("%s×%d", k, seen[k]))
		}
		summary := fmt.Sprintf("%d calls, %d distinct replicas (%s); response events=%d on every call — same data, any replica.",
			listRotationCalls, len(seen), strings.Join(uniques, ", "), eventCount)

		if len(seen) < 2 {
			return &demokit.StepResult{
				Status:  demokit.StatusWarning,
				Message: summary + " (only one replica reached — is the stack running with N>1? Verify with `docker compose ps event-server`)",
			}
		}
		return &demokit.StepResult{
			Status:  demokit.StatusSuccess,
			Message: summary,
		}
	}
}

// tenantRealm maps the demo's TENANT shorthand to a Keycloak realm
// name. Mirrors the Makefile's tenant-realm function; rewritten as a
// trivial switch instead of arithmetic on the ASCII code, since the
// realm set is small and explicit reads better.
func tenantRealm(t string) string {
	switch strings.ToUpper(t) {
	case "A":
		return "asgard"
	case "B":
		return "babylon"
	case "C":
		return "camelot"
	case "ASGARD", "BABYLON", "CAMELOT":
		return strings.ToLower(t)
	default:
		return ""
	}
}

// acquireDemoToken does an OAuth 2.0 Resource Owner Password Credentials
// (RFC 6749 §4.3) grant against Keycloak. Mirrors poller/streamer's
// helper; trimmed because the walkthrough only needs the access token
// and never retries.
func acquireDemoToken(keycloakURL, realm, clientID, clientSecret, username, password string) (string, error) {
	endpoint := strings.TrimRight(keycloakURL, "/") + "/realms/" + realm + "/protocol/openid-connect/token"
	form := url.Values{
		"grant_type":    {"password"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"username":      {username},
		"password":      {password},
	}
	resp, err := http.PostForm(endpoint, form)
	if err != nil {
		return "", fmt.Errorf("POST %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}
	var t struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", fmt.Errorf("token response decode failed: %w", err)
	}
	if t.AccessToken == "" {
		return "", fmt.Errorf("token response missing access_token")
	}
	return t.AccessToken, nil
}

// envOr is defined in main.go (same package); reused here.
