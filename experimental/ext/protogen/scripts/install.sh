#!/usr/bin/env bash
# protogen: build + install the protoc plugin to $GOPATH/bin. Runner-agnostic.
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bash "$DIR/scripts/build.sh"
cd "$DIR"
go install ./cmd/protoc-gen-go-mcp
