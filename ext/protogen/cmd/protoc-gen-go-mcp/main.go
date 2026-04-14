// protoc-gen-go-mcp is a protoc plugin that generates mcpkit server and client
// bindings from annotated proto service definitions.
//
// Usage:
//
//	protoc --go-mcp_out=. --go-mcp_opt=package_suffix=mcp myservice.proto
package main

import (
	"flag"
	"strings"

	"google.golang.org/protobuf/compiler/protogen"

	"github.com/panyam/mcpkit/ext/protogen/generator"
)

func main() {
	var flags flag.FlagSet
	packageSuffix := flags.String("package_suffix", "mcp",
		"Suffix for the generated Go package name. Empty string generates into the same package.")
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
