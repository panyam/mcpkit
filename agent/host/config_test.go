package host

import "testing"

func TestConfigValidate_ConnectionsSupersedeModel(t *testing.T) {
	// connections-only config is valid without a Model block
	c := &Config{
		Connections: &ConnectionsConfig{
			Active:      "local",
			Connections: map[string]ConnectionConfig{"local": {Type: "lmstudio", Model: "m"}},
		},
		Servers: []ServerConfig{{ID: "s", URL: "http://x/mcp"}},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("connections-only config should validate: %v", err)
	}
	empty := &Config{Servers: []ServerConfig{{ID: "s", URL: "http://x/mcp"}}}
	if err := empty.Validate(); err == nil {
		t.Fatal("config with neither model nor connections should error")
	}
}
