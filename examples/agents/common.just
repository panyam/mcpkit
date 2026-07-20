# Shared recipes for agent examples. Import from an example justfile:
#   import '../common.just'
# `demo` resolves model/base-url from ../llm.json (or ../llm.local.json) when
# MODEL/BASE_URL are unset — see ../demo.sh. Keys are never in the file.

# Run the deterministic scripted scenario (no LLM)
agent:
    go run .

# Run against a live model (MODEL=<name> BASE_URL=<url> override llm.json)
demo:
    @../demo.sh

# Run the golden-transcript test
test:
    go test ./... -count=1
