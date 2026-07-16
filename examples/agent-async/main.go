package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/panyam/mcpkit/agent"
)

func main() {
	model := flag.String("model", "", "OpenAI-compatible model for a live run (default: deterministic stub)")
	baseURL := flag.String("base-url", "http://localhost:1234/v1", "model endpoint for --model")
	flag.Parse()

	out := &syncWriter{}
	var provider agent.Provider
	if *model != "" {
		p, err := agent.NewOpenAIProvider(agent.OpenAIConfig{BaseURL: *baseURL, Model: *model})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		provider = p
	}
	if err := runScenario(out, provider); err != nil {
		fmt.Fprintln(os.Stderr, "agent-async:", err)
		os.Exit(1)
	}
	fmt.Print(out.String())
}

// syncWriter is a concurrency-safe buffer: the proactive trigger turn writes
// from the event-consumer goroutine while the main flow writes prompt lines.
type syncWriter struct {
	mu sync.Mutex
	b  []byte
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.b = append(w.b, p...)
	return len(p), nil
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.b)
}

// waitForOutput blocks until want appears in the writer or the deadline
// passes (the proactive trigger turn is asynchronous).
func waitForOutput(w interface{ String() string }, want string, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(w.String(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
