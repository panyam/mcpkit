#!/usr/bin/env bash
# protogen: build the protoc plugin. Runner-agnostic.
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR"
go build -o bin/protoc-gen-go-mcp ./cmd/protoc-gen-go-mcp
