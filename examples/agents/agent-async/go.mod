module github.com/panyam/mcpkit/examples/agents/agent-async

go 1.26.4

require (
	github.com/panyam/mcpkit v0.4.0-b2
	github.com/panyam/mcpkit/agent v0.0.0
	github.com/panyam/mcpkit/agent/host v0.0.0
	github.com/panyam/mcpkit/experimental/ext/events v0.0.0
	github.com/panyam/mcpkit/ext/tasks v0.0.0
)

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/fernet/fernet-go v0.0.0-20240119011108-303da6aec611 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/golang-jwt/jwt/v5 v5.2.2 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/invopop/jsonschema v0.13.0 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/panyam/gocurrent v0.1.1 // indirect
	github.com/panyam/goutils v0.1.8 // indirect
	github.com/panyam/mcpkit/experimental/ext/events/clients/go v0.0.0 // indirect
	github.com/panyam/mcpkit/ext/auth v0.0.0 // indirect
	github.com/panyam/mcpkit/ext/skills v0.0.0 // indirect
	github.com/panyam/oneauth v0.1.31 // indirect
	github.com/panyam/servicekit v0.1.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	github.com/wk8/go-ordered-map/v2 v2.1.8 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0 // indirect
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc v1.81.1 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/panyam/mcpkit => ../../..

replace github.com/panyam/mcpkit/agent => ../../../agent

replace github.com/panyam/mcpkit/agent/host => ../../../agent/host

replace github.com/panyam/mcpkit/ext/tasks => ../../../ext/tasks

replace github.com/panyam/mcpkit/ext/skills => ../../../ext/skills

replace github.com/panyam/mcpkit/ext/auth => ../../../ext/auth

replace github.com/panyam/mcpkit/ext/otel => ../../../ext/otel

replace github.com/panyam/mcpkit/experimental/ext/events => ../../../experimental/ext/events

replace github.com/panyam/mcpkit/experimental/ext/events/clients/go => ../../../experimental/ext/events/clients/go
