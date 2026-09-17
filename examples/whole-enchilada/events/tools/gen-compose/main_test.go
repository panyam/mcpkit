package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestInlinedNginxConfEscapesComposeVars guards the failure that shipped in
// ceaa017e: Compose interpolates the whole file, `configs:` content included,
// and resolves an unset variable to the empty string instead of erroring. An
// nginx config inlined with bare $host / $upstream therefore arrives as
// `proxy_set_header Host ;` and nginx exits 1 at startup.
//
// `docker compose config` does not catch this. The file resolves; only the
// content has been hollowed out. So the assertion has to live here.
func TestInlinedNginxConfEscapesComposeVars(t *testing.T) {
	ctx := tmplCtx{N: 3, EventServers: seq(3)}
	conf, err := renderString(nginxTmpl, ctx)
	if err != nil {
		t.Fatalf("render nginx: %v", err)
	}
	inlined := escapeComposeVars(conf)

	// Every nginx runtime variable the config relies on must survive as a
	// literal `$` for nginx, which means `$$` in the compose source.
	for _, v := range []string{"host", "upstream", "idx", "rest"} {
		if !strings.Contains(inlined, "$$"+v) {
			t.Errorf("nginx variable $%s is not escaped as $$%s; Compose will interpolate it away", v, v)
		}
	}

	// And no bare single-$ variable may remain. Scanning for "$" not followed
	// by another "$" catches a partial escape, which is the shape a future
	// edit is most likely to introduce.
	for i := 0; i < len(inlined); i++ {
		if inlined[i] != '$' {
			continue
		}
		if i+1 >= len(inlined) || inlined[i+1] != '$' {
			t.Fatalf("unescaped $ at offset %d: %q", i, excerpt(inlined, i))
		}
		i++ // consume the pair
	}
}

// TestNginxTemplateKeepsSingleDollar is the counter-test. nginx.tmpl must stay
// plain nginx syntax so it is readable and editable as nginx config, with
// escapeComposeVars the single place that knows about Compose. Escaping in the
// template would work today (the inlined copy is the only consumer) and would
// silently break the moment anything renders it for nginx to read directly.
func TestNginxTemplateKeepsSingleDollar(t *testing.T) {
	ctx := tmplCtx{N: 3, EventServers: seq(3)}
	conf, err := renderString(nginxTmpl, ctx)
	if err != nil {
		t.Fatalf("render nginx: %v", err)
	}
	if strings.Contains(conf, "$$") {
		t.Error("nginx.tmpl must keep single $; escaping belongs in escapeComposeVars, not the template")
	}
	if !strings.Contains(conf, "$host") {
		t.Error("expected bare $host in the on-disk nginx.conf")
	}
}

func excerpt(s string, at int) string {
	lo := max(0, at-40)
	hi := min(len(s), at+40)
	return s[lo:hi]
}

// TestCanonicalStackIsSelfContained pins the property the whole merge exists
// for: events-stack.yaml has to work when curl'd on its own, by someone with
// no checkout. Anything that reaches back into the repo defeats that, and the
// two ways to do it accidentally are a `build:` stanza and a relative bind
// mount. Both are easy to reintroduce by copying a service definition out of
// the dev overlay.
func TestCanonicalStackIsSelfContained(t *testing.T) {
	out := renderStack(t)

	if strings.Contains(out, "build:") {
		t.Error("events-stack.yaml must not build anything; the dev overlay owns build: stanzas")
	}
	// A bind mount is `- ./something:/container/path`. Named volumes have no
	// leading dot, and the inlined nginx config is a `configs:` entry.
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ./") || strings.HasPrefix(trimmed, "- ../") {
			t.Errorf("events-stack.yaml bind-mounts a repo path, which breaks a standalone curl: %q", trimmed)
		}
	}
	if !strings.Contains(out, "image: ghcr.io/") {
		t.Error("events-stack.yaml should pull published images")
	}
}

// TestDevOverlayBuildsEveryReplica catches the overlay going stale against N.
// The canonical stack renders N replicas from EventServers; an overlay that
// builds only some of them leaves the rest silently pulling a published image
// while you think you are testing your checkout.
func TestDevOverlayBuildsEveryReplica(t *testing.T) {
	const n = 3
	ctx := tmplCtx{N: n, EventServers: seq(n)}
	out, err := renderString(devTmpl, ctx)
	if err != nil {
		t.Fatalf("render dev overlay: %v", err)
	}
	for i := 1; i <= n; i++ {
		want := fmt.Sprintf("event-server-%d:", i)
		if !strings.Contains(out, want) {
			t.Errorf("dev overlay does not override %s, so it would pull instead of build", want)
		}
	}
	if strings.Count(out, "dockerfile:") != n {
		t.Errorf("expected %d build stanzas, got %d", n, strings.Count(out, "dockerfile:"))
	}
}

func renderStack(t *testing.T) string {
	t.Helper()
	ctx := tmplCtx{
		N: 3, EventServers: seq(3),
		Image:         "ghcr.io/panyam/mcpkit-event-server:latest",
		KeycloakImage: "ghcr.io/panyam/mcpkit-events-keycloak:latest",
	}
	conf, err := renderString(nginxTmpl, ctx)
	if err != nil {
		t.Fatalf("render nginx: %v", err)
	}
	ctx.NginxConf = indent(escapeComposeVars(conf), "      ")
	out, err := renderString(stackTmpl, ctx)
	if err != nil {
		t.Fatalf("render stack: %v", err)
	}
	return out
}
