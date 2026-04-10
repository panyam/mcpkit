module github.com/panyam/mcpkit/cmd/testclient

go 1.26.1

require (
	github.com/panyam/mcpkit v0.0.0
	github.com/panyam/mcpkit/ext/auth v0.0.0
	github.com/panyam/oneauth v0.0.69
)

require (
	github.com/fernet/fernet-go v0.0.0-20240119011108-303da6aec611 // indirect
	github.com/golang-jwt/jwt/v5 v5.2.2 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/panyam/gocurrent v0.1.0 // indirect
	github.com/panyam/goutils v0.1.8 // indirect
	github.com/panyam/servicekit v0.0.22 // indirect
	golang.org/x/crypto v0.46.0 // indirect
	golang.org/x/oauth2 v0.34.0 // indirect
	golang.org/x/sys v0.39.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251029180050-ab9386a59fda // indirect
	google.golang.org/grpc v1.78.0 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
)

replace github.com/panyam/mcpkit => ../..

replace github.com/panyam/mcpkit/ext/auth => ../../ext/auth
