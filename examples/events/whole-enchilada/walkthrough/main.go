package main

import (
	"flag"
	"os"
)

func main() {
	serverURL := flag.String("url", envOr("MCP_URL", "http://localhost:8080"),
		"event-server URL (default nginx frontdoor in the compose stack)")
	receiverURL := flag.String("receiver", envOr("RECEIVER_URL", "http://localhost:9090"),
		"receiver URL the walkthrough subscribes its webhook to")
	flag.Parse()

	runDemo(*serverURL, *receiverURL)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
