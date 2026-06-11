// evctl is the operator CLI for the whole-enchilada event-server's
// dynamic-source admin API. Adds / removes / lists EventSources on
// specific replicas via path-routed nginx endpoints under
// /admin/replicas/{idx}/sources/...
//
// Operator-side fan-out: --replicas 1,4 tells the CLI which replicas
// to address; each replica's admin API is hit independently. There's
// no cross-replica gossip — each event-server only knows about its
// own runtime-added sources. evctl prints per-replica responses so
// the operator sees divergence directly.
//
// Default target is http://localhost:9090 (nginx frontdoor); no
// /etc/hosts entries required.
//
// Usage:
//
//	evctl sources add discord --token=T --channels=A,B --tenants=a,c --replicas=1,4 [--name=discord.message]
//	evctl sources rm <name> --replicas=1,4
//	evctl sources list [--replicas=1,4]   # omit --replicas to query replica 1 only
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const usage = `evctl — dynamic-source admin CLI for the whole-enchilada event-server.

Usage:
  evctl sources add discord --token=T --channels=A,B [--tenants=a,c] --replicas=1,4 [--name=discord.message]
  evctl sources rm <name> --replicas=1,4
  evctl sources list [--replicas=1] [--target=http://localhost:9090]

Flags:
  --target    nginx frontdoor URL (default http://localhost:9090)
  --replicas  comma-separated replica indices (e.g. 1,4). Required for add/rm; defaults to 1 for list.
  --tenants   round-robin tenant tags applied to each yielded event (add only).
  --channels  Discord channel IDs the bot listens to (add discord only).
  --token     Discord bot token (add discord only). Never logged.
  --name      EventDef name override (default "discord.message", add only).
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "sources":
		if len(os.Args) < 3 {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		switch os.Args[2] {
		case "add":
			cmdAdd(os.Args[3:])
		case "rm":
			cmdRemove(os.Args[3:])
		case "list":
			cmdList(os.Args[3:])
		default:
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
	case "-h", "--help", "help":
		fmt.Fprint(os.Stdout, usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

// --- sources add discord ------------------------------------------

func cmdAdd(args []string) {
	if len(args) < 1 || args[0] != "discord" {
		fmt.Fprintln(os.Stderr, "evctl: only `discord` source type is supported today")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("sources add discord", flag.ExitOnError)
	target := fs.String("target", envOr("EVCTL_TARGET", "http://localhost:9090"), "nginx frontdoor URL")
	token := fs.String("token", os.Getenv("DISCORD_BOT_TOKEN"), "Discord bot token (or DISCORD_BOT_TOKEN env)")
	channels := fs.String("channels", "", "comma-separated Discord channel IDs")
	tenants := fs.String("tenants", "asgard,babylon,camelot", "comma-separated tenant tags (round-robin)")
	replicas := fs.String("replicas", "", "comma-separated replica indices (REQUIRED)")
	name := fs.String("name", "discord.message", "EventDef name to register the source under")
	_ = fs.Parse(args[1:])

	if *token == "" {
		fmt.Fprintln(os.Stderr, "evctl: --token (or DISCORD_BOT_TOKEN) is required")
		os.Exit(2)
	}
	chans := splitNonEmpty(*channels)
	if len(chans) == 0 {
		fmt.Fprintln(os.Stderr, "evctl: --channels must list at least one ID")
		os.Exit(2)
	}
	replicaIDs, err := parseReplicas(*replicas)
	if err != nil {
		fmt.Fprintln(os.Stderr, "evctl: --replicas:", err)
		os.Exit(2)
	}
	body := map[string]any{
		"bot_token":   *token,
		"channel_ids": chans,
		"tenants":     splitNonEmpty(*tenants),
		"name":        *name,
	}
	bodyJSON, _ := json.Marshal(body)

	fanOut(*target, replicaIDs, func(replica int) (string, error) {
		url := fmt.Sprintf("%s/admin/replicas/%d/sources/discord", strings.TrimRight(*target, "/"), replica)
		return postJSON(url, bodyJSON)
	})
}

// --- sources rm ---------------------------------------------------

func cmdRemove(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "evctl: rm <name> is required")
		os.Exit(2)
	}
	name := args[0]
	fs := flag.NewFlagSet("sources rm", flag.ExitOnError)
	target := fs.String("target", envOr("EVCTL_TARGET", "http://localhost:9090"), "nginx frontdoor URL")
	replicas := fs.String("replicas", "", "comma-separated replica indices (REQUIRED)")
	_ = fs.Parse(args[1:])

	replicaIDs, err := parseReplicas(*replicas)
	if err != nil {
		fmt.Fprintln(os.Stderr, "evctl: --replicas:", err)
		os.Exit(2)
	}

	fanOut(*target, replicaIDs, func(replica int) (string, error) {
		url := fmt.Sprintf("%s/admin/replicas/%d/sources/%s", strings.TrimRight(*target, "/"), replica, name)
		return doMethod(http.MethodDelete, url, nil)
	})
}

// --- sources list -------------------------------------------------

func cmdList(args []string) {
	fs := flag.NewFlagSet("sources list", flag.ExitOnError)
	target := fs.String("target", envOr("EVCTL_TARGET", "http://localhost:9090"), "nginx frontdoor URL")
	replicas := fs.String("replicas", "1", "comma-separated replica indices to query (default 1)")
	_ = fs.Parse(args)

	replicaIDs, err := parseReplicas(*replicas)
	if err != nil {
		fmt.Fprintln(os.Stderr, "evctl: --replicas:", err)
		os.Exit(2)
	}

	fanOut(*target, replicaIDs, func(replica int) (string, error) {
		url := fmt.Sprintf("%s/admin/replicas/%d/sources", strings.TrimRight(*target, "/"), replica)
		return doMethod(http.MethodGet, url, nil)
	})
}

// --- fan-out core --------------------------------------------------

func fanOut(target string, replicas []int, fn func(replica int) (string, error)) {
	var failed bool
	for _, r := range replicas {
		fmt.Printf("=== replica %d ===\n", r)
		body, err := fn(r)
		if err != nil {
			failed = true
			fmt.Fprintf(os.Stderr, "  ERROR: %v\n", err)
			continue
		}
		body = strings.TrimSpace(body)
		if body == "" {
			fmt.Println("  (empty response)")
			continue
		}
		fmt.Println(indent(body, "  "))
	}
	if failed {
		os.Exit(1)
	}
}

func postJSON(url string, body []byte) (string, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	return do(req)
}

func doMethod(method, url string, body []byte) (string, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return "", err
	}
	return do(req)
}

func do(req *http.Request) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("HTTP %d %s: %s", resp.StatusCode, resp.Request.URL.Path, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}

// --- helpers ------------------------------------------------------

func parseReplicas(csv string) ([]int, error) {
	if csv == "" {
		return nil, fmt.Errorf("missing comma-separated replica indices")
	}
	parts := splitNonEmpty(csv)
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid replica index %q: %v", p, err)
		}
		if n < 1 {
			return nil, fmt.Errorf("replica index must be >= 1, got %d", n)
		}
		out = append(out, n)
	}
	return out, nil
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, raw := range strings.Split(s, ",") {
		if v := strings.TrimSpace(raw); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
