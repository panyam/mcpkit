file-inputs walkthrough fixture
================================

This file is shipped as a SEP-2356 demo payload. The walkthrough reads it
via go:embed, encodes it as a `data:text/plain;name=README.txt;base64,…`
data URI, and passes that string to the `process_any_file` tool.

Edit it freely — the walkthrough re-reads the embedded bytes at compile
time, so changes take effect on the next `go run`.
