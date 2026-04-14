// Package generator implements the protoc-gen-go-mcp code generation logic.
// It reads proto service definitions with mcp_tool, mcp_resource, and
// mcp_prompt annotations, and emits Go registration functions that wire
// them into an mcpkit MCP server.
//
// The generator supports three forwarding variants (in-process, gRPC,
// ConnectRPC), configurable via [Config.Variants]. Templates are loaded
// from embedded .tmpl files via go:embed.
//
// Tool annotations support custom names, descriptions, timeouts,
// structured output with result summary templates, and namespace prefixing.
// Resource annotations support both static URIs and RFC 6570 URI templates.
// Prompt arguments are auto-derived from request message fields.
//
// See ext/protogen/docs/DESIGN.md for the full design document.
package generator

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/panyam/mcpkit/core"
	mcpv1 "github.com/panyam/mcpkit/ext/protogen/proto/mcp/v1"
	"github.com/panyam/mcpkit/ext/protogen/schema"
)

// Config holds generation options.
type Config struct {
	// PackageSuffix is appended to the Go package name. Default: "mcp".
	PackageSuffix string

	// Variants controls which registration flavors to generate.
	// Valid values: "inprocess", "grpc", "connect".
	// Default (nil or empty): all three.
	Variants map[string]bool
}

// DefaultVariants are emitted when no explicit variants are configured.
var DefaultVariants = map[string]bool{"inprocess": true, "grpc": true}

// HasVariant reports whether a variant should be generated.
func (c Config) HasVariant(name string) bool {
	if len(c.Variants) == 0 {
		return DefaultVariants[name]
	}
	return c.Variants[name]
}

// Generate processes a proto file and emits mcpkit registration code.
func Generate(gen *protogen.Plugin, file *protogen.File, cfg Config) {
	if len(file.Services) == 0 {
		return
	}

	suffix := cfg.PackageSuffix

	importPath := file.GoImportPath
	if suffix != "" {
		importPath += protogen.GoImportPath("/" + suffix)
	}

	gf := gen.NewGeneratedFile(
		file.GeneratedFilenamePrefix+".pb.mcp.go",
		importPath,
	)

	data, err := collectFileData(file, gf, cfg)
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
	SourcePath    string
	GoPackage     string
	PackageSuffix string
	GRPC          bool // emit gRPC forwarding variants
	Connect       bool // emit ConnectRPC forwarding variants
	Services      []serviceData
	Timestamp     string
}

// serviceData holds data for one proto service.
type serviceData struct {
	Name      string // Go name (e.g. "UserService")
	Namespace string // from mcp_service annotation
	Tools     []toolData
	Resources []resourceData
	Prompts   []promptData
}

// HasContent reports whether the service has any tools, resources, or prompts to generate.
func (sd serviceData) HasContent() bool {
	return len(sd.Tools) > 0 || len(sd.Resources) > 0 || len(sd.Prompts) > 0
}

// resourceData holds data for one MCP resource generated from a unary RPC.
type resourceData struct {
	MethodName   string   // Go method name (e.g. "GetUserProfile")
	URI          string   // URI or URI template from mcp_resource.uri_template
	Name         string   // display name
	MimeType     string   // content MIME type (default: "application/json")
	Description  string
	IsTemplate   bool     // true if URI contains template variables
	Params       []string // extracted template variable names
	RequestType  string   // qualified Go ident
	ResponseType string   // qualified Go ident
}

// promptData holds data for one MCP prompt generated from a unary RPC.
type promptData struct {
	MethodName   string       // Go method name
	PromptName   string       // MCP prompt name (snake_case)
	Description  string
	Arguments    []promptArg  // derived from request message fields
	RequestType  string       // qualified Go ident
	ResponseType string       // qualified Go ident
}

// promptArg describes a single prompt argument derived from a proto field.
type promptArg struct {
	Name        string // proto field name (JSON name)
	Description string // from field comment or empty
	Required    bool   // true for non-optional scalar fields
}

// toolData holds data for one MCP tool generated from a unary RPC.
type toolData struct {
	MethodName    string // Go method name (e.g. "GetUser")
	ToolName      string // MCP tool name (e.g. "get_user")
	Description   string
	Timeout       string // Go duration literal, empty if not set
	Structured    bool   // use StructuredResult instead of TextResult
	ResultSummary string // template for human-readable summary, empty if not set
	RequestType   string // qualified Go ident for the generated file
	ResponseType  string // qualified Go ident
	InputSchema   string // JSON string of the input schema
}

func collectFileData(file *protogen.File, gf *protogen.GeneratedFile, cfg Config) (fileData, error) {
	data := fileData{
		SourcePath:    file.Desc.Path(),
		GoPackage:     string(file.GoPackageName),
		PackageSuffix: cfg.PackageSuffix,
		GRPC:          cfg.HasVariant("grpc"),
		Connect:       cfg.HasVariant("connect"),
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
	}

	for _, svc := range file.Services {
		sd, err := collectServiceData(svc, gf)
		if err != nil {
			return fileData{}, err
		}
		if sd.HasContent() {
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

		// Read annotations — a method has at most one of mcp_tool, mcp_resource, mcp_prompt.
		toolOpts := mcpv1.GetToolOptions(method.Desc.Options())
		resOpts := mcpv1.GetResourceOptions(method.Desc.Options())
		promptOpts := mcpv1.GetPromptOptions(method.Desc.Options())

		annotCount := 0
		if toolOpts != nil {
			annotCount++
		}
		if resOpts != nil {
			annotCount++
		}
		if promptOpts != nil {
			annotCount++
		}
		if annotCount > 1 {
			return sd, fmt.Errorf("method %s: cannot have multiple MCP annotations (tool/resource/prompt)", method.GoName)
		}

		// Use qualified Go idents so the template gets proper imports.
		reqType := gf.QualifiedGoIdent(method.Input.GoIdent)
		respType := gf.QualifiedGoIdent(method.Output.GoIdent)

		if resOpts != nil {
			rd, err := collectResource(method, resOpts, reqType, respType)
			if err != nil {
				return sd, err
			}
			sd.Resources = append(sd.Resources, rd)
			continue
		}

		if promptOpts != nil {
			pd := collectPrompt(method, promptOpts, sd.Namespace, reqType, respType)
			sd.Prompts = append(sd.Prompts, pd)
			continue
		}

		toolName, err := resolveToolName(sd.Namespace, method, toolOpts)
		if err != nil {
			return sd, fmt.Errorf("method %s: %w", method.GoName, err)
		}

		// Check for duplicate tool names.
		if existing, ok := seenNames[toolName]; ok {
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

		// Result summary template (only meaningful with structured output).
		var resultSummary string
		if toolOpts != nil && toolOpts.ResultSummary != "" {
			resultSummary = toolOpts.ResultSummary
		}

		sd.Tools = append(sd.Tools, toolData{
			MethodName:    method.GoName,
			ToolName:      toolName,
			Description:   desc,
			Timeout:       timeout,
			Structured:    structured,
			ResultSummary: resultSummary,
			RequestType:   reqType,
			ResponseType:  respType,
			InputSchema:   string(schemaJSON),
		})
	}

	return sd, nil
}

func collectPrompt(method *protogen.Method, opts *mcpv1.MCPPromptOptions, namespace, reqType, respType string) promptData {
	name := MethodToSnakeCase(method.GoName)
	if opts.Name != "" {
		name = opts.Name
	}
	name = PrefixWithNamespace(namespace, name)

	desc := CleanComment(string(method.Comments.Leading))
	if opts.Description != "" {
		desc = opts.Description
	}

	// Derive prompt arguments from request message fields.
	var args []promptArg
	fields := method.Input.Desc.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		required := !fd.HasOptionalKeyword() && fd.IsList() == false && fd.IsMap() == false
		args = append(args, promptArg{
			Name:        string(fd.Name()),
			Description: CleanComment(string(method.Input.Fields[i].Comments.Leading)),
			Required:    required,
		})
	}

	return promptData{
		MethodName:   method.GoName,
		PromptName:   name,
		Description:  desc,
		Arguments:    args,
		RequestType:  reqType,
		ResponseType: respType,
	}
}

func collectResource(method *protogen.Method, opts *mcpv1.MCPResourceOptions, reqType, respType string) (resourceData, error) {
	if opts.URITemplate == "" {
		return resourceData{}, fmt.Errorf("method %s: mcp_resource.uri_template is required", method.GoName)
	}

	desc := CleanComment(string(method.Comments.Leading))
	if opts.Description != "" {
		desc = opts.Description
	}

	mimeType := opts.MimeType
	if mimeType == "" {
		mimeType = "application/json"
	}

	params := core.URITemplateVars(opts.URITemplate)

	return resourceData{
		MethodName:   method.GoName,
		URI:          opts.URITemplate,
		Name:         opts.Name,
		MimeType:     mimeType,
		Description:  desc,
		IsTemplate:   len(params) > 0,
		Params:       params,
		RequestType:  reqType,
		ResponseType: respType,
	}, nil
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
