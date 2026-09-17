// gen-compose renders the whole-enchilada docker-compose.yaml and
// nginx/nginx.conf from embedded templates for arbitrary replica
// counts of the event-server tier (-n).
//
// The output mirrors the *.whole-enchilada DNS naming convention:
//
//	event-server.whole-enchilada    — round-robin pool of all N event-server replicas
//	event-server-<i>.whole-enchilada — direct pin to replica i (1..N)
//
// Synthetic event producers (chat / presence) used to live in an in-
// compose push-server tier; they're now operator-runnable host drivers
// under drivers/synth/ (one binary, vocab selected via --event).
// push-server source code stays in push-server/ as a reference for the
// production-shape HTTPSource pattern but is not part of the default
// compose.
//
// Shared backends (keycloak, postgres, redis) and the observability
// stack (grafana, loki, tempo, mimir, otel-collector) live in sibling
// composes under docker/ and are reached by bare container names on
// the shared `mcpkit` network — they don't get `.whole-enchilada`
// aliases.
package main

import (
	"bytes"
	_ "embed"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed dev.tmpl
var devTmpl string

//go:embed nginx.tmpl
var nginxTmpl string

//go:embed stack.tmpl
var stackTmpl string

type tmplCtx struct {
	N                int    // event-server replica count
	Image            string // published event-server image ref, standalone stack only
	KeycloakImage    string // published keycloak-with-realms image ref, standalone stack only
	NginxConf        string // rendered nginx.conf, pre-indented for inlining as a compose config
	InjectBearer     string // shared secret env-default
	KCResourceSecret string // pre-baked client_secret for the mcp-event-server confidential client in all realms (DEMO ONLY — rotate in production)
	EventServers     []int  // 1..N
}

func main() {
	n := flag.Int("n", 3, "event-server replica count (>=1)")
	outDir := flag.String("out", ".", "leaf directory (whole-enchilada root) to render into")
	image := flag.String("image", "ghcr.io/panyam/mcpkit-event-server:latest",
		"published event-server image for the standalone stack")
	kcImage := flag.String("keycloak-image", "ghcr.io/panyam/mcpkit-events-keycloak:latest",
		"published keycloak-with-realms image for the standalone stack's auth profile")
	flag.Parse()

	if *n < 1 {
		log.Fatalf("gen-compose: n must be >= 1 (got n=%d)", *n)
	}

	ctx := tmplCtx{
		N:                *n,
		InjectBearer:     "stage-1-shared-secret",
		KCResourceSecret: "mcpkit-demo-secret-DEMO-ONLY",
		EventServers:     seq(*n),
		Image:            *image,
		KeycloakImage:    *kcImage,
	}

	// The nginx config is inlined as a compose `configs:` entry rather than
	// written to disk and bind-mounted, because a curl'd compose file has no
	// sibling files to mount. Nothing reads nginx/nginx.conf any more, so it
	// is no longer emitted.
	nginxConf, err := renderString(nginxTmpl, ctx)
	if err != nil {
		log.Fatalf("render nginx: %v", err)
	}
	ctx.NginxConf = indent(escapeComposeVars(nginxConf), "      ")
	if err := render(stackTmpl, ctx, filepath.Join(*outDir, "events-stack.yaml")); err != nil {
		log.Fatalf("render stack: %v", err)
	}
	if err := render(devTmpl, ctx, filepath.Join(*outDir, "compose.dev.yaml")); err != nil {
		log.Fatalf("render dev overlay: %v", err)
	}

	fmt.Fprintf(os.Stderr, "gen-compose: rendered N=%d event-servers into %s (events-stack.yaml, compose.dev.yaml)\n", *n, *outDir)
}

func render(tmpl string, ctx tmplCtx, out string) error {
	t, err := template.New(out).Parse(tmpl)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	return t.Execute(f, ctx)
}

// renderString executes a template into a string instead of a file, so the
// result can be embedded in another template.
func renderString(tmpl string, ctx tmplCtx) (string, error) {
	t, err := template.New("inline").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, ctx); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// escapeComposeVars doubles every `$` so Compose emits it literally.
//
// Compose interpolates the WHOLE file, `configs:` content included, and an
// unset variable resolves to the empty string rather than an error. nginx
// configs are full of bare variables ($host, $upstream, $idx, $rest), so
// without this the inlined config ships as `proxy_set_header Host ;` and
// nginx exits 1 on startup. `docker compose config` does not catch it: the
// file resolves fine, it is the content that has been hollowed out.
//
// Applies only to the inlined copy. nginx/nginx.conf on disk is bind-mounted
// by the dev overlay and must keep single `$`.
func escapeComposeVars(s string) string {
	return strings.ReplaceAll(s, "$", "$$")
}

// indent prefixes every non-empty line with pad. Blank lines are left bare
// rather than padded, because trailing whitespace in a YAML block scalar is
// preserved and shows up as noise in the generated file.
func indent(s, pad string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = pad + ln
	}
	return strings.Join(lines, "\n")
}

func seq(n int) []int {
	out := make([]int, n)
	for i := 0; i < n; i++ {
		out[i] = i + 1
	}
	return out
}
