package host

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/panyam/mcpkit/agent"
	"github.com/panyam/mcpkit/core"
)

// terminalElicitationUI renders elicitations as line prompts. At any prompt
// the user can type /d (decline) or /c (cancel); those become the result
// action, not errors, matching the coordinator contract. Schema-driven:
// enum properties become numbered choices, booleans y/n, numbers parsed,
// everything else free text. Properties prompt in sorted order because the
// schema's JSON object carries none. URL-mode requests print the URL and
// wait for completion.
func terminalElicitationUI(in *bufio.Reader, out io.Writer) agent.ElicitationUI {
	return func(ctx context.Context, req core.ElicitationRequest) (core.ElicitationResult, error) {
		fmt.Fprintf(out, "\n? %s\n", req.Message)

		if req.Mode == "url" {
			fmt.Fprintf(out, "  open: %s\n  press enter when done (/d decline, /c cancel): ", req.URL)
			line, err := readLine(in)
			if err != nil {
				return core.ElicitationResult{Action: "cancel"}, nil
			}
			if action, done := controlAction(line); done {
				return core.ElicitationResult{Action: action}, nil
			}
			return core.ElicitationResult{Action: "accept"}, nil
		}

		props, required, err := parseSchema(req.RequestedSchema)
		if err != nil {
			return core.ElicitationResult{}, err
		}
		if len(props) == 0 {
			fmt.Fprint(out, "  accept? (y/n, /c cancel): ")
			line, err := readLine(in)
			if err != nil {
				return core.ElicitationResult{Action: "cancel"}, nil
			}
			if action, done := controlAction(line); done {
				return core.ElicitationResult{Action: action}, nil
			}
			if strings.HasPrefix(strings.ToLower(line), "y") {
				return core.ElicitationResult{Action: "accept"}, nil
			}
			return core.ElicitationResult{Action: "decline"}, nil
		}

		content := map[string]any{}
		for _, p := range props {
			value, action, err := promptProperty(in, out, p, required[p.name])
			if err != nil {
				return core.ElicitationResult{}, err
			}
			if action != "" {
				return core.ElicitationResult{Action: action}, nil
			}
			if value != nil {
				content[p.name] = value
			}
		}
		return core.ElicitationResult{Action: "accept", Content: content}, nil
	}
}

type property struct {
	name  string
	typ   string
	enum  []string
	title string
}

func parseSchema(raw json.RawMessage) ([]property, map[string]bool, error) {
	required := map[string]bool{}
	if len(raw) == 0 {
		return nil, required, nil
	}
	var schema struct {
		Properties map[string]struct {
			Type  string `json:"type"`
			Enum  []any  `json:"enum"`
			Title string `json:"title"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, nil, fmt.Errorf("agentchat: bad requestedSchema: %w", err)
	}
	for _, r := range schema.Required {
		required[r] = true
	}
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	props := make([]property, 0, len(names))
	for _, name := range names {
		p := schema.Properties[name]
		prop := property{name: name, typ: p.Type, title: p.Title}
		for _, e := range p.Enum {
			prop.enum = append(prop.enum, fmt.Sprint(e))
		}
		props = append(props, prop)
	}
	return props, required, nil
}

func promptProperty(in *bufio.Reader, out io.Writer, p property, required bool) (any, string, error) {
	label := p.name
	if p.title != "" {
		label = p.title
	}

	for {
		switch {
		case len(p.enum) > 0:
			for i, opt := range p.enum {
				fmt.Fprintf(out, "  %d) %s\n", i+1, opt)
			}
			fmt.Fprintf(out, "  %s (1-%d): ", label, len(p.enum))
		case p.typ == "boolean":
			fmt.Fprintf(out, "  %s (y/n): ", label)
		default:
			fmt.Fprintf(out, "  %s (%s): ", label, orDefault(p.typ, "text"))
		}

		line, err := readLine(in)
		if err != nil {
			return nil, "cancel", nil
		}
		if action, done := controlAction(line); done {
			return nil, action, nil
		}
		if line == "" {
			if required {
				fmt.Fprintln(out, "  (required)")
				continue
			}
			return nil, "", nil
		}

		switch {
		case len(p.enum) > 0:
			idx, err := strconv.Atoi(line)
			if err != nil || idx < 1 || idx > len(p.enum) {
				fmt.Fprintln(out, "  (pick a listed number)")
				continue
			}
			return p.enum[idx-1], "", nil
		case p.typ == "boolean":
			return strings.HasPrefix(strings.ToLower(line), "y"), "", nil
		case p.typ == "number" || p.typ == "integer":
			n, err := strconv.ParseFloat(line, 64)
			if err != nil {
				fmt.Fprintln(out, "  (not a number)")
				continue
			}
			if p.typ == "integer" {
				return int(n), "", nil
			}
			return n, "", nil
		default:
			return line, "", nil
		}
	}
}

func controlAction(line string) (string, bool) {
	switch strings.TrimSpace(line) {
	case "/d":
		return "decline", true
	case "/c":
		return "cancel", true
	}
	return "", false
}

func readLine(in *bufio.Reader) (string, error) {
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
