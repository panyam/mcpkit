package main

import "flag"

// conformanceGroups adds a nested group to the fixture for the groups
// conformance scenario (make testconf-groups, #1708). The demo has no nested
// groups, and without one the scenario's nesting checks can only SKIP.
var conformanceGroups = flag.Bool("conformance-groups", false,
	"add the disputes child group under billing and users, for the groups conformance scenario")
