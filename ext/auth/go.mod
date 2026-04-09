module github.com/panyam/mcpkit/ext/auth

go 1.26.1

require (
	github.com/golang-jwt/jwt/v5 v5.2.2
	github.com/panyam/gocurrent v0.1.0
	github.com/panyam/mcpkit v0.0.0
	github.com/panyam/oneauth v0.0.68
)

require (
	github.com/fernet/fernet-go v0.0.0-20240119011108-303da6aec611 // indirect
	golang.org/x/crypto v0.46.0 // indirect
	golang.org/x/oauth2 v0.34.0 // indirect
)

replace github.com/panyam/mcpkit => ../../
