# examples/common/playground.just
#
# Shared justfile fragment for browser-ready examples (#1719). An example
# with web/playground.json imports it:
#
#   import '../common/playground.just'

# Serve this example's browser playground on http://localhost:8788/
serve-web port="8788":
    bash {{source_directory()}}/../../scripts/serve-example-wasm.sh {{justfile_directory()}} {{port}}
