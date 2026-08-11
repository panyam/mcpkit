module github.com/panyam/mcpkit/experimental/agent/surfaces/web

go 1.26.5

require (
	connectrpc.com/connect v1.19.2
	github.com/panyam/mcpkit v0.5.1
	github.com/panyam/mcpkit/experimental/agent v0.0.0
	github.com/panyam/mcpkit/experimental/agent/host v0.0.0
	github.com/panyam/mcpkit/experimental/agent/surfaces v0.0.0
	github.com/panyam/servicekit v0.1.4
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/fernet/fernet-go v0.0.0-20240119011108-303da6aec611 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/mattn/go-sqlite3 v1.14.22 // indirect
	github.com/panyam/gocurrent v0.1.2 // indirect
	github.com/panyam/goutils v0.1.8 // indirect
	github.com/panyam/mcpkit/experimental/agent/store/gorm v0.0.0 // indirect
	github.com/panyam/mcpkit/experimental/agent/store/redis v0.0.0 // indirect
	github.com/panyam/mcpkit/experimental/ext/agents v0.0.0 // indirect
	github.com/panyam/mcpkit/experimental/ext/agents/clients/go v0.0.0 // indirect
	github.com/panyam/mcpkit/experimental/ext/events v0.0.0 // indirect
	github.com/panyam/mcpkit/experimental/ext/events/clients/go v0.0.0 // indirect
	github.com/panyam/mcpkit/ext/auth v0.0.0 // indirect
	github.com/panyam/mcpkit/ext/skills v0.0.0 // indirect
	github.com/panyam/oneauth v0.1.36 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/pgvector/pgvector-go v0.4.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/redis/go-redis/v9 v9.21.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.opentelemetry.io/otel v1.44.0 // indirect
	go.opentelemetry.io/otel/trace v1.44.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.2 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.39.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc v1.83.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	gorm.io/driver/postgres v1.6.2 // indirect
	gorm.io/driver/sqlite v1.6.0 // indirect
	gorm.io/gorm v1.31.2 // indirect
)

replace github.com/panyam/mcpkit => ../../../..

replace github.com/panyam/mcpkit/experimental/agent => ../../

replace github.com/panyam/mcpkit/experimental/agent/host => ../../host

replace github.com/panyam/mcpkit/experimental/agent/surfaces => ..

replace github.com/panyam/mcpkit/experimental/agent/store/redis => ../../store/redis

replace github.com/panyam/mcpkit/experimental/agent/store/gorm => ../../store/gorm

replace github.com/panyam/mcpkit/ext/auth => ../../../../ext/auth

replace github.com/panyam/mcpkit/ext/skills => ../../../../ext/skills

replace github.com/panyam/mcpkit/ext/tasks => ../../../../ext/tasks

replace github.com/panyam/mcpkit/experimental/ext/agents => ../../../../experimental/ext/agents

replace github.com/panyam/mcpkit/experimental/ext/agents/clients/go => ../../../../experimental/ext/agents/clients/go

replace github.com/panyam/mcpkit/experimental/ext/events => ../../../../experimental/ext/events

replace github.com/panyam/mcpkit/experimental/ext/events/clients/go => ../../../../experimental/ext/events/clients/go
