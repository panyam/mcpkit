package host

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/client"
)

// Config is agentchat's JSON configuration. Secrets are never inlined:
// apiKeyEnv / authTokenEnv name environment variables to read at startup,
// following the same indirection convention the rest of the repo's example
// configs use.
type Config struct {
	// Model configures the OpenAI-compatible endpoint.
	Model ModelConfig `json:"model"`

	// Instructions is the system prompt. Optional.
	Instructions string `json:"instructions,omitempty"`

	// MaxSteps caps model calls per turn. Zero uses the agent default.
	MaxSteps int `json:"maxSteps,omitempty"`

	// Servers lists the MCP servers to connect.
	Servers []ServerConfig `json:"servers"`

	// Triggers lists proactive-turn bindings over the configured event
	// streams.
	Triggers []TriggerConfig `json:"triggers,omitempty"`

	// MetaTools exposes the async control-plane tools to the model
	// (subscribe_events, create_trigger, list_tasks, cancel_task, ...).
	// Auto-implied when any server has events or triggers configured.
	MetaTools bool `json:"metaTools,omitempty"`

	// TaskGraceSec is how long a task-backed tool call stays inline before
	// detaching to the background (completion arrives as injected context
	// and a transcript line; /tasks manages running tasks). Zero uses the
	// 10s default; negative disables detaching (wait inline forever).
	TaskGraceSec int `json:"taskGraceSec,omitempty"`
}

// taskGrace resolves the configured grace window.
func (c *Config) taskGrace() time.Duration {
	switch {
	case c.TaskGraceSec < 0:
		return 0
	case c.TaskGraceSec == 0:
		return client.DefaultTaskGrace
	default:
		return time.Duration(c.TaskGraceSec) * time.Second
	}
}

// ModelConfig points at an OpenAI-compatible chat-completions endpoint.
type ModelConfig struct {
	BaseURL string `json:"baseUrl"`
	Model   string `json:"model"`
	// APIKeyEnv names the environment variable holding the bearer key.
	// Empty means unauthenticated (local servers).
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`

	// Backup, when set, wraps the model in a FailoverProvider: a call
	// that fails cleanly on the primary retries here once, and the
	// primary is benched for a cooldown. See /health in the REPL.
	Backup *BackupModelConfig `json:"backup,omitempty"`
}

// BackupModelConfig is the failover endpoint (same shape as the primary,
// minus further nesting).
type BackupModelConfig struct {
	BaseURL   string `json:"baseUrl"`
	Model     string `json:"model"`
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
}

// ServerConfig is one MCP server connection.
type ServerConfig struct {
	// ID is the source identifier (used for collision-qualified tool
	// names). Must not contain underscores.
	ID string `json:"id"`

	// URL is the MCP endpoint (streamable HTTP).
	URL string `json:"url"`

	// Auth configures how to authenticate to this server. Nil means
	// unauthenticated.
	Auth *AuthConfig `json:"auth,omitempty"`

	// Allow, when non-empty, restricts this server to the named tools
	// (a FilterSource capability boundary, not a display preference).
	Allow []string `json:"allow,omitempty"`

	// Skills controls SEP-2640 skill loading for this server. Nil or true
	// auto-detects (servers without the capability are skipped silently);
	// false opts out even when the server advertises skills.
	Skills *bool `json:"skills,omitempty"`

	// Events lists the event streams to open on this server. Each event
	// feeds the injection policy (and any trigger bindings that match).
	Events []EventConfig `json:"events,omitempty"`
}

// EventConfig subscribes one event name and optionally overrides its
// context hint (host config wins over the server-advertised _meta hint).
type EventConfig struct {
	Name string `json:"name"`

	// Hint overrides how occurrences reach the model (priority,
	// aggregation window, template, retention, sensitivity).
	Hint *agent.ContextHint `json:"hint,omitempty"`
}

// TriggerConfig declares one proactive-turn binding, mediated by the
// anti-nag policy (one firing per binding until user engagement plus the
// cooldown, session budget on top).
type TriggerConfig struct {
	// Server and Event select the stream; Server empty matches any.
	Server string `json:"server,omitempty"`
	Event  string `json:"event"`

	// Filter is a set of top-level payload field equality checks (all
	// must match). The config-file rendition of the code-level filter
	// hook; embedders wanting richer predicates use the agent package
	// directly.
	Filter map[string]string `json:"filter,omitempty"`

	// Instructions seed the proactive turn.
	Instructions string `json:"instructions"`

	// Label names the binding in transcripts and logs.
	Label string `json:"label"`

	// CooldownSec is the re-arm floor in seconds (0 = default 300).
	CooldownSec int `json:"cooldownSec,omitempty"`
}

// AuthConfig selects one of the client auth modes MCP supports. Secrets are
// env-indirected like everything else in this config.
type AuthConfig struct {
	// Type is "bearer" (static token), "client-credentials" (OAuth
	// machine-to-machine via PRM/AS discovery), or "oauth"
	// (authorization-code browser flow; not implemented yet, tracked in
	// the agent epic).
	Type string `json:"type"`

	// TokenEnv names the env var holding the static bearer token.
	// Required for type "bearer".
	TokenEnv string `json:"tokenEnv,omitempty"`

	// ClientIDEnv / ClientSecretEnv name the env vars holding the OAuth
	// client credentials. Required for type "client-credentials".
	ClientIDEnv     string `json:"clientIdEnv,omitempty"`
	ClientSecretEnv string `json:"clientSecretEnv,omitempty"`

	// Scopes to request for OAuth types. Empty inherits the server's PRM
	// scopes_supported.
	Scopes []string `json:"scopes,omitempty"`

	// AllowInsecure permits an http:// authorization server (dev/test
	// only; production AS endpoints must be https).
	AllowInsecure bool `json:"allowInsecure,omitempty"`
}

// Validate checks mode-specific requirements and that named env vars are
// actually set, so misconfiguration fails at startup rather than as a
// mid-conversation 401.
func (a *AuthConfig) Validate() error {
	switch a.Type {
	case "bearer":
		if a.TokenEnv == "" {
			return fmt.Errorf("auth type bearer requires tokenEnv")
		}
		if os.Getenv(a.TokenEnv) == "" {
			return fmt.Errorf("auth env %s is not set", a.TokenEnv)
		}
	case "client-credentials":
		if a.ClientIDEnv == "" || a.ClientSecretEnv == "" {
			return fmt.Errorf("auth type client-credentials requires clientIdEnv and clientSecretEnv")
		}
		for _, env := range []string{a.ClientIDEnv, a.ClientSecretEnv} {
			if os.Getenv(env) == "" {
				return fmt.Errorf("auth env %s is not set", env)
			}
		}
	case "oauth":
		return fmt.Errorf("auth type oauth (authorization-code browser flow) is not implemented yet (tracked as mcpkit issue 907); use bearer or client-credentials")
	default:
		return fmt.Errorf("unknown auth type %q (want bearer or client-credentials)", a.Type)
	}
	return nil
}

// LoadConfig reads and validates a config file.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("agentchat: parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("agentchat: %s: %w", path, err)
	}
	return &cfg, nil
}

// Validate enforces the invariants the app relies on.
func (c *Config) Validate() error {
	if c.Model.BaseURL == "" || c.Model.Model == "" {
		return fmt.Errorf("model.baseUrl and model.model are required")
	}
	seen := map[string]bool{}
	for i, s := range c.Servers {
		if s.ID == "" || s.URL == "" {
			return fmt.Errorf("servers[%d]: id and url are required", i)
		}
		if seen[s.ID] {
			return fmt.Errorf("servers[%d]: duplicate id %q", i, s.ID)
		}
		seen[s.ID] = true
		if s.Auth != nil {
			if err := s.Auth.Validate(); err != nil {
				return fmt.Errorf("servers[%d] (%s): %w", i, s.ID, err)
			}
		}
	}
	return nil
}

// APIKey resolves the model bearer key from the environment. Empty when
// unset or unconfigured.
func (c *Config) APIKey() string {
	if c.Model.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(c.Model.APIKeyEnv)
}
