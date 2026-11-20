package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadCardSchema(t *testing.T) *CompiledSchema {
	t.Helper()
	raw, err := os.ReadFile("testdata/servercard/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	// The file only holds $defs. Upstream's validate-examples.ts checks
	// against #/$defs/ServerCard, so root the schema there too.
	schema["$ref"] = "#/$defs/ServerCard"
	cs, err := CompileSchema(schema)
	if err != nil {
		t.Fatalf("compile upstream schema: %v", err)
	}
	return cs
}

// The upstream examples are the reference: every valid one must decode,
// validate and survive a round trip that the schema still accepts, and every
// invalid one must be refused by decoding or by Validate.
func TestServerCardUpstreamExamples(t *testing.T) {
	schema := loadCardSchema(t)

	valid, _ := filepath.Glob("testdata/servercard/valid/*.json")
	invalid, _ := filepath.Glob("testdata/servercard/invalid/*.json")
	if len(valid) == 0 || len(invalid) == 0 {
		t.Fatal("vendored examples missing")
	}

	for _, path := range valid {
		t.Run("valid/"+filepath.Base(path), func(t *testing.T) {
			raw, _ := os.ReadFile(path)
			var card ServerCard
			if err := json.Unmarshal(raw, &card); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if err := card.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			out, _ := json.Marshal(card)
			if verr := schema.Validate(out); verr != nil {
				t.Fatalf("round trip fails the schema: %v\n%s", verr, out)
			}
			var before, after any
			_ = json.Unmarshal(raw, &before)
			_ = json.Unmarshal(out, &after)
			b1, _ := json.Marshal(before)
			b2, _ := json.Marshal(after)
			if string(b1) != string(b2) {
				t.Fatalf("round trip changed the card\nwant %s\ngot  %s", b1, b2)
			}
		})
	}

	for _, path := range invalid {
		t.Run("invalid/"+filepath.Base(path), func(t *testing.T) {
			raw, _ := os.ReadFile(path)
			if schema.Validate(raw) == nil {
				t.Fatal("upstream schema accepts an example filed as invalid")
			}
			var card ServerCard
			if err := json.Unmarshal(raw, &card); err != nil {
				return
			}
			if err := card.Validate(); err == nil {
				t.Fatal("Validate accepted an invalid card")
			}
		})
	}
}

func TestServerCardValidate(t *testing.T) {
	good := func() ServerCard {
		return ServerCard{
			Schema:      ServerCardSchemaURL,
			Name:        "io.github.acme/weather",
			Version:     "1.2.0",
			Description: "Forecasts",
			Remotes:     []Remote{{Type: RemoteTypeStreamableHTTP, URL: "https://acme.example/mcp"}},
		}
	}
	if err := (&ServerCard{}).Validate(); err == nil {
		t.Fatal("empty card passed")
	}
	cases := []struct {
		name   string
		mutate func(*ServerCard)
		want   string
	}{
		{"free-form name", func(c *ServerCard) { c.Name = "weather" }, "name"},
		{"two slashes", func(c *ServerCard) { c.Name = "a/b/c" }, "name"},
		{"date schema", func(c *ServerCard) {
			c.Schema = "https://static.modelcontextprotocol.io/schemas/2025-12-11/server-card.schema.json"
		}, "$schema"},
		{"empty description", func(c *ServerCard) { c.Description = "" }, "description"},
		{"101-char description", func(c *ServerCard) { c.Description = strings.Repeat("d", 101) }, "description"},
		{"version range", func(c *ServerCard) { c.Version = "^1.2.0" }, "range"},
		{"wildcard version", func(c *ServerCard) { c.Version = "1.x" }, "range"},
		{"empty version", func(c *ServerCard) { c.Version = "" }, "version"},
		{"websocket remote", func(c *ServerCard) { c.Remotes[0].Type = "websocket" }, "type"},
		{"ftp remote url", func(c *ServerCard) { c.Remotes[0].URL = "ftp://acme.example/mcp" }, "url"},
		{"repository without source", func(c *ServerCard) { c.Repository = &Repository{URL: "https://github.com/acme/w"} }, "repository"},
	}
	base := good()
	if err := base.Validate(); err != nil {
		t.Fatalf("baseline card: %v", err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := good()
			tc.mutate(&c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
		})
	}
	t.Run("templated remote url", func(t *testing.T) {
		c := good()
		c.Remotes[0].URL = "{base}/mcp"
		c.Remotes[0].Variables = map[string]Input{"base": {IsRequired: true}}
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
	})
}
