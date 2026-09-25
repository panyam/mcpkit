module github.com/panyam/mcpkit/tests/e2e

go 1.26.6

replace (
	github.com/panyam/mcpkit => ../..
	github.com/panyam/mcpkit/ext/auth => ../../ext/auth
	github.com/panyam/mcpkit/ext/ui => ../../ext/ui
)

require (
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/panyam/mcpkit v0.7.0
	github.com/panyam/mcpkit/ext/auth v0.0.0
	github.com/panyam/mcpkit/ext/ui v0.0.0
	github.com/panyam/oneauth v0.1.37
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/alexedwards/scs/v2 v2.9.0 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.6.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/fernet/fernet-go v0.0.0-20240119011108-303da6aec611 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/panyam/gocurrent v0.1.2 // indirect
	github.com/panyam/goutils v0.1.13 // indirect
	github.com/panyam/servicekit v0.1.5 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.opentelemetry.io/otel v1.46.0 // indirect
	go.opentelemetry.io/otel/trace v1.46.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.6 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260917231906-eeb232e0883d // indirect
	google.golang.org/grpc v1.84.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
