// gen-compose renders the whole-enchilada docker-compose.yaml and
// nginx/nginx.conf from embedded templates for arbitrary replica
// counts of the event-server tier (-n) and the push-server tier (-m).
//
// The output mirrors the *.whole_enchilada DNS naming convention:
//
//	event_server.whole_enchilada    — round-robin pool of all N event-server replicas
//	event_server_<i>.whole_enchilada — direct pin to replica i (1..N)
//	pusher.whole_enchilada           — round-robin pool of all M push-server replicas
//	pusher_<i>.whole_enchilada       — direct pin to replica i (1..M)
//	receiver.whole_enchilada         — example webhook consumer
//
// Stages 2+ enrich the same convention with admin.whole_enchilada,
// keycloak.whole_enchilada, grafana.whole_enchilada, loki.whole_enchilada,
// mimir.whole_enchilada, etc. The same template adds those blocks
// behind feature flags when later stages land.
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"text/template"
)

//go:embed compose.tmpl
var composeTmpl string

//go:embed nginx.tmpl
var nginxTmpl string

type tmplCtx struct {
	N             int    // event-server replica count
	M             int    // push-server replica count
	InjectBearer  string // shared secret env-default
	WebhookSecret string // receiver secret env-default
	EventServers  []int  // 1..N
	PushServers   []int  // 1..M
}

func main() {
	n := flag.Int("n", 1, "event-server replica count (>=1)")
	m := flag.Int("m", 1, "push-server replica count (>=1)")
	outDir := flag.String("out", ".", "leaf directory (whole-enchilada root) to render into")
	flag.Parse()

	if *n < 1 || *m < 1 {
		log.Fatalf("gen-compose: n and m must be >= 1 (got n=%d m=%d)", *n, *m)
	}

	ctx := tmplCtx{
		N:             *n,
		M:             *m,
		InjectBearer:  "stage-1-shared-secret",
		WebhookSecret: "whsec_demo_secret_change_me_in_production",
		EventServers:  seq(*n),
		PushServers:   seq(*m),
	}

	if err := render(composeTmpl, ctx, filepath.Join(*outDir, "docker-compose.yaml")); err != nil {
		log.Fatalf("render compose: %v", err)
	}
	if err := render(nginxTmpl, ctx, filepath.Join(*outDir, "nginx", "nginx.conf")); err != nil {
		log.Fatalf("render nginx: %v", err)
	}
	fmt.Fprintf(os.Stderr, "gen-compose: rendered N=%d event-servers, M=%d push-servers into %s\n", *n, *m, *outDir)
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

func seq(n int) []int {
	out := make([]int, n)
	for i := 0; i < n; i++ {
		out[i] = i + 1
	}
	return out
}
