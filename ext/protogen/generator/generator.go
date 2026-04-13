// Package generator implements the protoc-gen-go-mcp code generation logic.
// It transforms proto service definitions into mcpkit server/client registration code.
package generator

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/compiler/protogen"

	mcpv1 "github.com/panyam/mcpkit/ext/protogen/proto/mcp/v1"
	"github.com/panyam/mcpkit/ext/protogen/schema"
)

// Config holds generation options.
type Config struct {
	// PackageSuffix is appended to the Go package name. Default: "mcp".
	PackageSuffix string
}

// Generate processes a proto file and emits mcpkit registration code.
func Generate(gen *protogen.Plugin, file *protogen.File, cfg Config) {
	if len(file.Services) == 0 {
		return
	}

	suffix := cfg.PackageSuffix
	if suffix == "" {
		suffix = "mcp"
	}

	gf := gen.NewGeneratedFile(
		file.GeneratedFilenamePrefix+".pb.mcp.go",
		file.GoImportPath+protogen.GoImportPath("/"+suffix),
	)

	data, err := collectFileData(file, gf)
	if err != nil {
		gen.Error(fmt.Errorf("annotation error in %s: %w", file.Desc.Path(), err))
		return
	}
	if len(data.Services) == 0 {
		return
	}

	if err := fileTpl.Execute(gf, data); err != nil {
		gen.Error(fmt.Errorf("template execution failed for %s: %w", file.Desc.Path(), err))
	}
}

// fileData holds all data needed to render the template for one file.
type fileData struct {
	SourcePath string
	GoPackage  string
	Services   []serviceData
	Timestamp  string
}

// serviceData holds data for one proto service.
type serviceData struct {
	Name      string // Go name (e.g. "UserService")
	Namespace string // from mcp_service annotation
	Tools     []toolData
}

// toolData holds data for one MCP tool generated from a unary RPC.
type toolData struct {
	MethodName   string // Go method name (e.g. "GetUser")
	ToolName     string // MCP tool name (e.g. "get_user")
	Description  string
	Timeout      string // Go duration literal, empty if not set
	Structured   bool   // use StructuredResult instead of TextResult
	RequestType  string // qualified Go ident for the generated file
	ResponseType string // qualified Go ident
	InputSchema  string // JSON string of the input schema
}

func collectFileData(file *protogen.File, gf *protogen.GeneratedFile) (fileData, error) {
	data := fileData{
		SourcePath: file.Desc.Path(),
		GoPackage:  string(file.GoPackageName),
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}

	for _, svc := range file.Services {
		sd, err := collectServiceData(svc, gf)
		if err != nil {
			return fileData{}, err
		}
		if len(sd.Tools) > 0 {
			data.Services = append(data.Services, sd)
		}
	}

	return data, nil
}

func collectServiceData(svc *protogen.Service, gf *protogen.GeneratedFile) (serviceData, error) {
	sd := serviceData{
		Name: svc.GoName,
	}

	// Read mcp_service annotation for namespace.
	if svcOpts := mcpv1.GetServiceOptions(svc.Desc.Options()); svcOpts != nil {
		sd.Namespace = svcOpts.Namespace
	}

	seenNames := map[string]*protogen.Method{}

	for _, method := range svc.Methods {
		// Skip streaming methods — MCP tools are request-response.
		if method.Desc.IsStreamingClient() || method.Desc.IsStreamingServer() {
			continue
		}

		// Read mcp_tool annotation.
		toolOpts := mcpv1.GetToolOptions(method.Desc.Options())

		toolName, err := resolveToolName(sd.Namespace, method, toolOpts)
		if err != nil {
			return sd, fmt.Errorf("method %s: %w", method.GoName, err)
		}

		// Check for duplicate tool names.
		if existing, ok := seenNames[toolName]; ok {
			// Log warning but continue — don't fail the whole generation.
			fmt.Fprintf(gf, "// WARNING: duplicate tool name %q from methods %s and %s\n",
				toolName, existing.GoName, method.GoName)
			continue
		}
		seenNames[toolName] = method

		// Description: annotation overrides comment.
		desc := CleanComment(string(method.Comments.Leading))
		if toolOpts != nil && toolOpts.Description != "" {
			desc = toolOpts.Description
		}

		// Structured output.
		structured := false
		if toolOpts != nil {
			structured = toolOpts.StructuredOutput
		}

		// Timeout: validate at generation time, emit as nanosecond literal.
		var timeout string
		if toolOpts != nil && toolOpts.Timeout != "" {
			d, err := time.ParseDuration(toolOpts.Timeout)
			if err != nil {
				return sd, fmt.Errorf("method %s: invalid timeout %q: %w", method.GoName, toolOpts.Timeout, err)
			}
			timeout = fmt.Sprintf("time.Duration(%d) /* %s */", int64(d), d)
		}

		inputSchema := schema.FromMessage(method.Input.Desc)
		schemaJSON, _ := json.Marshal(inputSchema)

		// Use qualified Go idents so the template gets proper imports.
		reqType := gf.QualifiedGoIdent(method.Input.GoIdent)
		respType := gf.QualifiedGoIdent(method.Output.GoIdent)

		sd.Tools = append(sd.Tools, toolData{
			MethodName:   method.GoName,
			ToolName:     toolName,
			Description:  desc,
			Timeout:      timeout,
			Structured:   structured,
			RequestType:  reqType,
			ResponseType: respType,
			InputSchema:  string(schemaJSON),
		})
	}

	return sd, nil
}

func resolveToolName(namespace string, method *protogen.Method, opts *mcpv1.MCPToolOptions) (string, error) {
	var name string
	if opts != nil && opts.Name != "" {
		if err := ValidateToolName(opts.Name); err != nil {
			return "", fmt.Errorf("invalid mcp_tool.name: %w", err)
		}
		name = opts.Name
	} else {
		name = MethodToSnakeCase(method.GoName)
	}
	return PrefixWithNamespace(namespace, name), nil
}

// escapeString escapes a string for use in a Go string literal.
func escapeString(s string) string {
	return strings.ReplaceAll(
		strings.ReplaceAll(s, `\`, `\\`),
		`"`, `\"`,
	)
}
