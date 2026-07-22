module github.com/panyam/mcpkit/cmd/testclient

go 1.26.4

require (
	github.com/panyam/mcpkit v0.4.0-b3
	github.com/panyam/mcpkit/ext/auth v0.0.0
	github.com/panyam/oneauth v0.1.36
)

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/fernet/fernet-go v0.0.0-20240119011108-303da6aec611 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/panyam/gocurrent v0.1.1 // indirect
	github.com/panyam/goutils v0.1.8 // indirect
	github.com/panyam/servicekit v0.1.2 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.2 // indirect
	golang.org/x/crypto v0.46.0 // indirect
	golang.org/x/oauth2 v0.34.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.33.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260120221211-b8f7ae30c516 // indirect
	google.golang.org/grpc v1.80.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/panyam/mcpkit => ../..

replace github.com/panyam/mcpkit/ext/auth => ../../ext/auth
