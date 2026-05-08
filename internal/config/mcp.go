package config

import (
	"encoding/json"
	"os"
	"strings"
)

type MCPServerConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

type MCPConfig struct {
	Servers map[string]MCPServerConfig `json:"mcpServers"`
}

// LoadMCP reads .mcp.json and expands ${VAR} references from the environment.
// Returns an empty config (no servers) if the file does not exist.
func LoadMCP(path string) (*MCPConfig, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &MCPConfig{Servers: map[string]MCPServerConfig{}}, nil
	}
	if err != nil {
		return nil, err
	}

	var cfg MCPConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	for name, srv := range cfg.Servers {
		expanded := make(map[string]string, len(srv.Env))
		for k, v := range srv.Env {
			expanded[k] = expandEnv(v)
		}
		srv.Env = expanded
		cfg.Servers[name] = srv
	}

	return &cfg, nil
}

// LoadPrompt reads a prompt file and returns its content.
// Falls back to fallback if the file does not exist.
func LoadPrompt(path, fallback string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	return strings.TrimSpace(string(data))
}

func expandEnv(s string) string {
	return os.Expand(s, func(key string) string {
		return os.Getenv(key)
	})
}
