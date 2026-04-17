module github.com/panyam/mcpkit/tests/e2e

go 1.26.1

replace (
	github.com/panyam/mcpkit => ../..
	github.com/panyam/mcpkit/ext/auth => ../../ext/auth
	github.com/panyam/mcpkit/ext/ui => ../../ext/ui
)

require (
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/panyam/mcpkit v0.2.3
	github.com/panyam/mcpkit/ext/auth v0.0.0
	github.com/panyam/mcpkit/ext/ui v0.0.0
	github.com/panyam/oneauth v0.0.73
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/alexedwards/scs/v2 v2.8.0 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.1 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/fernet/fernet-go v0.0.0-20240119011108-303da6aec611 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/invopop/jsonschema v0.13.0 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/panyam/gocurrent v0.1.0 // indirect
	github.com/panyam/goutils v0.1.8 // indirect
	github.com/panyam/servicekit v0.0.25 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/wk8/go-ordered-map/v2 v2.1.8 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/crypto v0.46.0 // indirect
	golang.org/x/oauth2 v0.34.0 // indirect
	golang.org/x/sys v0.39.0 // indirect
	golang.org/x/text v0.32.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251029180050-ab9386a59fda // indirect
	google.golang.org/grpc v1.78.0 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
