// Package generator implements the protoc-gen-go-mcp code generation logic.
// It transforms proto service definitions into mcpkit server/client registration code.
package generator

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/compiler/protogen"

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

	data := collectFileData(file, gf)
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

func collectFileData(file *protogen.File, gf *protogen.GeneratedFile) fileData {
	data := fileData{
		SourcePath: file.Desc.Path(),
		GoPackage:  string(file.GoPackageName),
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
	}

	for _, svc := range file.Services {
		sd := collectServiceData(svc, gf)
		if len(sd.Tools) > 0 {
			data.Services = append(data.Services, sd)
		}
	}

	return data
}

func collectServiceData(svc *protogen.Service, gf *protogen.GeneratedFile) serviceData {
	sd := serviceData{
		Name: svc.GoName,
	}

	// TODO: read mcp_service annotation for namespace once proto extensions are wired.

	seenNames := map[string]*protogen.Method{}

	for _, method := range svc.Methods {
		// Skip streaming methods — MCP tools are request-response.
		if method.Desc.IsStreamingClient() || method.Desc.IsStreamingServer() {
			continue
		}

		toolName := resolveToolName(sd.Namespace, method)

		// Check for duplicate tool names.
		if existing, ok := seenNames[toolName]; ok {
			// Log warning but continue — don't fail the whole generation.
			fmt.Fprintf(gf, "// WARNING: duplicate tool name %q from methods %s and %s\n",
				toolName, existing.GoName, method.GoName)
			continue
		}
		seenNames[toolName] = method

		desc := CleanComment(string(method.Comments.Leading))
		inputSchema := schema.FromMessage(method.Input.Desc)
		schemaJSON, _ := json.Marshal(inputSchema)

		// Use qualified Go idents so the template gets proper imports.
		reqType := gf.QualifiedGoIdent(method.Input.GoIdent)
		respType := gf.QualifiedGoIdent(method.Output.GoIdent)

		sd.Tools = append(sd.Tools, toolData{
			MethodName:   method.GoName,
			ToolName:     toolName,
			Description:  desc,
			RequestType:  reqType,
			ResponseType: respType,
			InputSchema:  string(schemaJSON),
		})
	}

	return sd
}

func resolveToolName(namespace string, method *protogen.Method) string {
	// TODO: check mcp_tool annotation for custom name.
	name := MethodToSnakeCase(method.GoName)
	return PrefixWithNamespace(namespace, name)
}

// escapeString escapes a string for use in a Go string literal.
func escapeString(s string) string {
	return strings.ReplaceAll(
		strings.ReplaceAll(s, `\`, `\\`),
		`"`, `\"`,
	)
}
