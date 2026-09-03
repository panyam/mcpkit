package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/ext/auth"
	"github.com/panyam/mcpkit/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeScopeLookup implements auth.ScopeLookup over plain maps. Template
// matching is a prefix stand-in; the real ordering-and-precedence contract is
// exercised against *server.Registry in the registry tests.
type fakeScopeLookup struct {
	tools     map[string]core.ToolDef
	resources map[string]core.ResourceDef
	templates map[string]core.ResourceTemplate // key: URI prefix to match
	prompts   map[string]core.PromptDef
}

func (f fakeScopeLookup) ToolDef(n string) (core.ToolDef, bool) { d, ok := f.tools[n]; return d, ok }
func (f fakeScopeLookup) ResourceDef(u string) (core.ResourceDef, bool) {
	d, ok := f.resources[u]
	return d, ok
}
func (f fakeScopeLookup) PromptDef(n string) (core.PromptDef, bool) {
	d, ok := f.prompts[n]
	return d, ok
}
func (f fakeScopeLookup) ResourceTemplateDefFor(u string) (core.ResourceTemplate, bool) {
	for prefix, def := range f.templates {
		if len(u) >= len(prefix) && u[:len(prefix)] == prefix {
			return def, true
		}
	}
	return core.ResourceTemplate{}, false
}

func req(method, params string) *core.Request {
	return &core.Request{
		ID:     json.RawMessage(`1`),
		Method: method,
		Params: core.NewRawJSON(json.RawMessage(params)),
	}
}

// run drives the middleware and reports whether it short-circuited, plus the
// challenge header when it did.
func run(t *testing.T, mw server.Middleware, ctx context.Context, r *core.Request) (passed bool, header string) {
	t.Helper()
	next := server.MiddlewareFunc(func(context.Context, *core.Request) (*core.Response, error) {
		return core.NewResponse(r.ID, "ok"), nil
	})
	resp, err := mw(ctx, r, next)
	if err == nil {
		require.NotNil(t, resp)
		return true, ""
	}
	var authErr *core.AuthError
	require.True(t, errors.As(err, &authErr), "expected *core.AuthError, got %T", err)
	assert.Equal(t, http.StatusForbidden, authErr.Code)
	return false, authErr.WWWAuthenticate
}

// Every primitive #481 exercises must gate, not just tools/call. Before this,
// a scope-gated resource or prompt was simply unguarded.
func TestScopeMiddleware_GatesAllPrimitives(t *testing.T) {
	lookup := fakeScopeLookup{
		tools: map[string]core.ToolDef{
			"admin_call": {Name: "admin_call", ScopeChallenge: core.RequireScopes("admin-write")},
		},
		resources: map[string]core.ResourceDef{
			"file:///secret": {URI: "file:///secret", ScopeChallenge: core.RequireScopes("files:read")},
		},
		templates: map[string]core.ResourceTemplate{
			"db://": {URITemplate: "db://{table}", ScopeChallenge: core.RequireScopes("db:read")},
		},
		prompts: map[string]core.PromptDef{
			"secret_prompt": {Name: "secret_prompt", ScopeChallenge: core.RequireScopes("prompts:read")},
		},
	}
	mw := auth.NewScopeMiddleware(lookup, auth.WithResourceMetadataURL("https://rs/.well-known/prm"))

	cases := []struct {
		name, method, params, wantScope string
	}{
		{"tool", "tools/call", `{"name":"admin_call"}`, "admin-write"},
		{"exact resource", "resources/read", `{"uri":"file:///secret"}`, "files:read"},
		{"templated resource", "resources/read", `{"uri":"db://users"}`, "db:read"},
		{"prompt", "prompts/get", `{"name":"secret_prompt"}`, "prompts:read"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/denied", func(t *testing.T) {
			passed, hdr := run(t, mw, withClaims(context.Background(), "unrelated"), req(tc.method, tc.params))
			assert.False(t, passed, "must not reach the handler")
			assert.Contains(t, hdr, `error="insufficient_scope"`)
			assert.Contains(t, hdr, `scope="`+tc.wantScope+`"`)
			assert.Contains(t, hdr, `resource_metadata="https://rs/.well-known/prm"`)
		})
		t.Run(tc.name+"/allowed", func(t *testing.T) {
			passed, _ := run(t, mw, withClaims(context.Background(), tc.wantScope), req(tc.method, tc.params))
			assert.True(t, passed, "properly scoped caller must reach the handler")
		})
	}
}

// Exact resources beat templates, matching dispatcher resolution. Gating one
// definition while the dispatcher executes another is how authorization bugs
// get introduced.
func TestScopeMiddleware_ExactResourceBeatsTemplate(t *testing.T) {
	lookup := fakeScopeLookup{
		resources: map[string]core.ResourceDef{
			"db://public": {URI: "db://public"}, // no gate
		},
		templates: map[string]core.ResourceTemplate{
			"db://": {URITemplate: "db://{table}", ScopeChallenge: core.RequireScopes("db:read")},
		},
	}
	mw := auth.NewScopeMiddleware(lookup)

	passed, _ := run(t, mw, withClaims(context.Background()), req("resources/read", `{"uri":"db://public"}`))
	assert.True(t, passed, "exact ungated resource must win over the gated template")

	passed, _ = run(t, mw, withClaims(context.Background()), req("resources/read", `{"uri":"db://private"}`))
	assert.False(t, passed, "unmatched by exact, so the template gate applies")
}

// The reason the callback exists: required scope as a function of the
// arguments. GitHub's workflow scope depends on which paths a commit touches.
func TestScopeMiddleware_ArgumentDependentChallenge(t *testing.T) {
	lookup := fakeScopeLookup{tools: map[string]core.ToolDef{
		"put_file": {Name: "put_file", ScopeChallenge: func(ctx context.Context, r *core.Request) (*core.ScopeChallenge, error) {
			var p struct {
				Arguments struct {
					Path string `json:"path"`
				} `json:"arguments"`
			}
			if err := r.Params.Bind(&p); err != nil {
				return nil, err
			}
			need := "repo"
			if len(p.Arguments.Path) >= 18 && p.Arguments.Path[:18] == ".github/workflows/" {
				need = "workflow"
			}
			if core.HasScope(ctx, need) {
				return nil, nil
			}
			return &core.ScopeChallenge{Scopes: []string{need}}, nil
		}},
	}}
	mw := auth.NewScopeMiddleware(lookup)
	ctx := withClaims(context.Background(), "repo") // has repo, not workflow

	passed, _ := run(t, mw, ctx, req("tools/call", `{"name":"put_file","arguments":{"path":"README.md"}}`))
	assert.True(t, passed, "ordinary path needs only repo, which the caller holds")

	passed, hdr := run(t, mw, ctx, req("tools/call", `{"name":"put_file","arguments":{"path":".github/workflows/ci.yml"}}`))
	assert.False(t, passed, "workflow path must escalate")
	assert.Contains(t, hdr, `scope="workflow"`)
}

// A callback that cannot decide must refuse, never wave the caller through.
func TestScopeMiddleware_CallbackErrorFailsClosed(t *testing.T) {
	lookup := fakeScopeLookup{tools: map[string]core.ToolDef{
		"flaky": {Name: "flaky", ScopeChallenge: func(context.Context, *core.Request) (*core.ScopeChallenge, error) {
			return nil, errors.New("permission service unreachable")
		}},
	}}
	passed, hdr := run(t, auth.NewScopeMiddleware(lookup), withClaims(context.Background(), "anything"),
		req("tools/call", `{"name":"flaky"}`))
	assert.False(t, passed, "an undecidable callback must fail closed")
	assert.Contains(t, hdr, `error="insufficient_scope"`)
}

// Adding the middleware to an existing server must change nothing until a
// definition opts in.
func TestScopeMiddleware_UngatedPrimitivesPassThrough(t *testing.T) {
	lookup := fakeScopeLookup{
		tools:     map[string]core.ToolDef{"open": {Name: "open"}},
		resources: map[string]core.ResourceDef{"file:///x": {URI: "file:///x"}},
		prompts:   map[string]core.PromptDef{"p": {Name: "p"}},
	}
	mw := auth.NewScopeMiddleware(lookup)
	ctx := withClaims(context.Background()) // no scopes at all

	for _, tc := range []struct{ method, params string }{
		{"tools/call", `{"name":"open"}`},
		{"resources/read", `{"uri":"file:///x"}`},
		{"prompts/get", `{"name":"p"}`},
		{"resources/list", `{}`},
		{"initialize", `{}`},
	} {
		passed, _ := run(t, mw, ctx, req(tc.method, tc.params))
		assert.True(t, passed, "%s must pass through ungated", tc.method)
	}
}

// The deprecated fields keep working unchanged, including the OR-hierarchy
// semantics and the least-privilege advertisement.
func TestScopeMiddleware_LegacyStaticFieldsStillHonored(t *testing.T) {
	lookup := fakeScopeLookup{tools: map[string]core.ToolDef{
		"legacy": {Name: "legacy", RequiredScopes: []string{"admin-write"}, AcceptedScopes: []string{"admin-write", "admin"}},
	}}
	mw := auth.NewScopeMiddleware(lookup)

	passed, _ := run(t, mw, withClaims(context.Background(), "admin"), req("tools/call", `{"name":"legacy"}`))
	assert.True(t, passed, "parent scope satisfies the OR hierarchy")

	passed, hdr := run(t, mw, withClaims(context.Background(), "other"), req("tools/call", `{"name":"legacy"}`))
	assert.False(t, passed)
	assert.Contains(t, hdr, `scope="admin-write"`, "challenge advertises RequiredScopes only, never AcceptedScopes")
}

// ScopeChallenge wins when both are set, so migration is a field at a time.
func TestScopeMiddleware_CallbackOverridesLegacyFields(t *testing.T) {
	lookup := fakeScopeLookup{tools: map[string]core.ToolDef{
		"both": {
			Name:           "both",
			RequiredScopes: []string{"old-scope"},
			ScopeChallenge: core.RequireScopes("new-scope"),
		},
	}}
	_, hdr := run(t, auth.NewScopeMiddleware(lookup), withClaims(context.Background(), "unrelated"),
		req("tools/call", `{"name":"both"}`))
	assert.Contains(t, hdr, `scope="new-scope"`)
	assert.NotContains(t, hdr, "old-scope")
}
