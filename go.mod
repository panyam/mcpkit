module github.com/panyam/mcpkit

go 1.25.0

require (
	github.com/panyam/gocurrent v0.0.15
	github.com/panyam/servicekit v0.0.18
	github.com/stretchr/testify v1.10.0
)

// TODO: Remove replace after servicekit PR #19 is merged and released
replace github.com/panyam/servicekit => ../../servicekit/master

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/panyam/goutils v0.1.8 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/sys v0.38.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251029180050-ab9386a59fda // indirect
	google.golang.org/grpc v1.78.0 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
