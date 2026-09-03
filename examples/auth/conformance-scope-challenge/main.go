// Command conformance-scope-challenge is an mcpkit SUT for the upstream
// SEP-2350 server scope-challenge scenario (modelcontextprotocol/conformance
// PR 481), which asserts that a server refuses an under-scoped call with an
// RFC 6750 §3.1 challenge and succeeds on retry with an upgraded token.
//
// The scenario is SDK-neutral: it drives opaque bearer tokens and grades the
// wire, so it never inspects a JWT and needs no authorization server. That is
// why this SUT string-matches two fixed tokens instead of validating
// signatures. For the real-IdP side of the story see ../step-up, which runs
// the same wire against Keycloak and Okta.
//
// Every fixture requires two scopes, one for the method and one for the
// specific object, which is how the scenario checks that a server advertises
// the complete set in a single challenge rather than drip-feeding one per
// round trip.
//
// The template fixture is the interesting one. Its required scope is
// mcp:conformance:resources:template:123, where 123 is the expanded URI
// variable, so the requirement is a function of the request rather than a
// property of the registration. That cannot be expressed by declaring scopes
// up front, and it is why core.ScopeChallengeFunc receives the request.
//
//	go run ./conformance-scope-challenge -addr :3040
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"

	mcpcommon "github.com/panyam/mcpkit/examples/common"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
)

// Fixed tokens from the upstream scenario's SUT contract.
const (
	lowToken  = "mcp-conformance-scope-low"
	fullToken = "mcp-conformance-scope-full"
)

const (
	scopeToolsCall   = "mcp:conformance:tools:call"
	scopeToolSimple  = "mcp:conformance:tools:test_simple_text"
	scopeResRead     = "mcp:conformance:resources:read"
	scopeResStatic   = "mcp:conformance:resources:static"
	scopeResTemplate = "mcp:conformance:resources:template:" // + expanded id
	scopePromptsGet  = "mcp:conformance:prompts:get"
	scopePromptSimpl = "mcp:conformance:prompts:test_simple_prompt"
)

// fullScopes is everything the scenario's upgraded token must carry. The
// template scope is pinned to the id the scenario uses (test://template/123/data).
var fullScopes = []string{
	scopeToolsCall, scopeToolSimple,
	scopeResRead, scopeResStatic, scopeResTemplate + "123",
	scopePromptsGet, scopePromptSimpl,
}

// fixtureValidator authenticates the scenario's two opaque tokens. The
// under-scoped token authenticates successfully and simply holds no useful
// scope, which is what makes the refusal a 403 rather than a 401.
type fixtureValidator struct{ prmURL string }

func (v *fixtureValidator) token(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func (v *fixtureValidator) Validate(r *http.Request) error {
	switch v.token(r) {
	case lowToken, fullToken:
		return nil
	}
	return &core.AuthError{
		Code:            http.StatusUnauthorized,
		Message:         "unauthorized",
		WWWAuthenticate: fmt.Sprintf(`Bearer resource_metadata="%s"`, v.prmURL),
	}
}

func (v *fixtureValidator) Claims(r *http.Request) *core.Claims {
	switch tok := v.token(r); tok {
	case fullToken:
		return &core.Claims{Subject: "conformance", Scopes: fullScopes}
	case lowToken:
		// Authenticated, but holds nothing any fixture asks for.
		return &core.Claims{Subject: "conformance", Scopes: []string{"mcp:conformance:none"}}
	}
	return nil
}

// needAll refuses unless the caller holds every listed scope, advertising the
// complete set in one challenge.
func needAll(scopes ...string) core.ScopeChallengeFunc {
	return func(ctx context.Context, _ *core.Request) (*core.ScopeChallenge, error) {
		for _, s := range scopes {
			if !core.HasScope(ctx, s) {
				return &core.ScopeChallenge{Scopes: scopes}, nil
			}
		}
		return nil, nil
	}
}

func main() {
	addr := flag.String("addr", ":3040", "listen address")
	wire := mcpcommon.RegisterWireFlags(flag.CommandLine)
	flag.Parse()

	listenURL := "http://localhost" + *addr
	prmURL := listenURL + "/.well-known/oauth-protected-resource/mcp"
	validator := &fixtureValidator{prmURL: prmURL}

	log.Printf("conformance-scope-challenge: serving %s/mcp", listenURL)
	log.Printf("  under-scoped token: %s", lowToken)
	log.Printf("  full-scope token:   %s", fullToken)

	cfg := mcpcommon.ServerConfig{
		Name:    "mcpkit-conformance-scope-challenge",
		Version: "0.1.0",
		Addr:    *addr,
		Wire:    wire,
		Options: []server.Option{server.WithAuth(validator)},
		Register: func(srv *server.Server) {
			srv.Register(core.TextTool[struct{}]("test_simple_text",
				"Conformance fixture tool.",
				func(core.ToolContext, struct{}) (string, error) { return "ok", nil },
				core.WithToolScopeChallenge(needAll(scopeToolsCall, scopeToolSimple)),
			))

			srv.RegisterResource(
				core.ResourceDef{
					URI:            "test://static-text",
					Name:           "static-text",
					MimeType:       "text/plain",
					ScopeChallenge: needAll(scopeResRead, scopeResStatic),
				},
				func(ctx core.ResourceContext, req core.ResourceRequest) (core.ResourceResult, error) {
					return core.ResourceResult{Contents: []core.ResourceReadContent{{
						URI: "test://static-text", MimeType: "text/plain", Text: "static fixture",
					}}}, nil
				},
			)

			srv.RegisterResourceTemplate(
				core.ResourceTemplate{
					URITemplate: "test://template/{id}/data",
					Name:        "template-data",
					MimeType:    "text/plain",
					// Argument-dependent: the required scope carries the
					// expanded id, so it is derived from the request rather
					// than declared once at registration.
					ScopeChallenge: func(ctx context.Context, r *core.Request) (*core.ScopeChallenge, error) {
						var p struct {
							URI string `json:"uri"`
						}
						if err := r.Params.Bind(&p); err != nil {
							return nil, err
						}
						id := strings.TrimSuffix(strings.TrimPrefix(p.URI, "test://template/"), "/data")
						return needAll(scopeResRead, scopeResTemplate+id)(ctx, r)
					},
				},
				func(ctx core.ResourceContext, uri string, params map[string]string) (core.ResourceResult, error) {
					return core.ResourceResult{Contents: []core.ResourceReadContent{{
						URI: uri, MimeType: "text/plain",
						Text: fmt.Sprintf("template fixture for %s", params["id"]),
					}}}, nil
				},
			)

			srv.RegisterPrompt(
				core.PromptDef{
					Name:           "test_simple_prompt",
					Description:    "Conformance fixture prompt.",
					ScopeChallenge: needAll(scopePromptsGet, scopePromptSimpl),
				},
				func(ctx core.PromptContext, req core.PromptRequest) (core.PromptResponse, error) {
					return core.PromptResult{
						Description: "fixture",
						Messages: []core.PromptMessage{{
							Role:    "user",
							Content: core.Content{Type: "text", Text: "fixture prompt"},
						}},
					}, nil
				},
			)

			srv.UseMiddleware(auth.NewScopeMiddleware(srv.Registry(),
				auth.WithResourceMetadataURL(prmURL),
			))
		},
		TransportOptions: []server.TransportOption{
			server.WithMux(func(mux *http.ServeMux) {
				auth.MountAuth(mux, auth.AuthConfig{
					ResourceURI:     listenURL,
					ScopesSupported: fullScopes,
					MCPPath:         "/mcp",
				})
			}),
		},
	}
	if err := mcpcommon.RunServer(cfg); err != nil {
		log.Fatal(err)
	}
}
