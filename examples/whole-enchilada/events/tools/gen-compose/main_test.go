package main

import (
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

// TestNginxConfOnDiskKeepsSingleDollar is the counter-test. The dev overlay
// bind-mounts nginx/nginx.conf directly, where nginx reads the file itself and
// Compose never sees it. Escaping there would break the dev stack instead.
func TestNginxConfOnDiskKeepsSingleDollar(t *testing.T) {
	ctx := tmplCtx{N: 3, EventServers: seq(3)}
	conf, err := renderString(nginxTmpl, ctx)
	if err != nil {
		t.Fatalf("render nginx: %v", err)
	}
	if strings.Contains(conf, "$$") {
		t.Error("nginx.conf rendered for disk must keep single $; escaping is for the inlined copy only")
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
