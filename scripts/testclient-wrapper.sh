#!/bin/bash
# Wrapper script to capture testclient output for debugging conformance failures.
exec go run ./cmd/testclient "$@" 2>&1 | tee -a /tmp/testclient-conformance.log
