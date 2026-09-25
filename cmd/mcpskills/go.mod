module github.com/panyam/mcpkit/cmd/mcpskills

go 1.26.6

require (
	github.com/panyam/mcpkit v0.7.1
	github.com/panyam/mcpkit/cmd/common v0.7.1
	github.com/panyam/mcpkit/ext/skills v0.7.1
	github.com/spf13/cobra v1.10.2
)

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.6.1 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/panyam/gocurrent v0.1.2 // indirect
	github.com/panyam/goutils v0.1.13 // indirect
	github.com/panyam/servicekit v0.1.5 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.6 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/term v0.46.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260917231906-eeb232e0883d // indirect
	google.golang.org/grpc v1.84.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/panyam/mcpkit => ../..

replace github.com/panyam/mcpkit/cmd/common => ../common

replace github.com/panyam/mcpkit/ext/skills => ../../ext/skills
