// protoc-gen-go-mcp is a protoc plugin that generates mcpkit MCP server
// bindings from annotated proto service definitions.
//
// It reads mcp_tool, mcp_resource, and mcp_prompt annotations from proto
// methods and generates typed registration functions (in-process, gRPC
// forwarding, ConnectRPC forwarding) that wire proto services into an
// mcpkit MCP server.
//
// Usage with protoc:
//
//	protoc --go-mcp_out=. --go-mcp_opt=package_suffix=mcp myservice.proto
//
// Usage with buf (recommended):
//
//	# buf.gen.yaml
//	plugins:
//	  - local: protoc-gen-go-mcp
//	    out: gen
//	    opt:
//	      - paths=source_relative
//	      - variants=inprocess       # omit grpc/connect deps
//
// Options:
//   - package_suffix: Go package name suffix (default empty). Generates
//     into the same package as protoc-gen-go output. Set to "mcp" for a
//     separate sub-package.
//   - variants: Comma-separated list of registration variants to emit.
//     Valid: inprocess, grpc, connect. Default: inprocess,grpc.
package main

import (
	"flag"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/panyam/mcpkit/experimental/ext/protogen/generator"
)

func main() {
	var flags flag.FlagSet
	packageSuffix := flags.String("package_suffix", "",
		"Suffix for the generated Go package name. Default empty generates into the same package as pb.go.")
	variants := flags.String("variants", "",
		"Comma-separated list of registration variants to generate: inprocess,grpc,connect. Default: inprocess,grpc.")

	protogen.Options{
		ParamFunc: flags.Set,
	}.Run(func(gen *protogen.Plugin) error {
		cfg := generator.Config{
			PackageSuffix: *packageSuffix,
		}
		if *variants != "" {
			cfg.Variants = make(map[string]bool)
			for _, v := range strings.Split(*variants, ",") {
				v = strings.TrimSpace(v)
				if v != "" {
					cfg.Variants[v] = true
				}
			}
		}
		for _, file := range gen.Files {
			if !file.Generate {
				continue
			}
			generator.Generate(gen, file, cfg)
		}
		return nil
	})
}
