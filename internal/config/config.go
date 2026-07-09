// Package config loads projects.json (name -> local working directory)
// and small runtime settings for the bot/agent pair.
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

type Config struct {
	// Projects maps a short name (shown as a button in Telegram) to an
	// absolute path on the agent's machine where `claude` should run.
	Projects map[string]string `json:"projects"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if len(cfg.Projects) == 0 {
		return nil, fmt.Errorf("config has no projects defined")
	}
	return &cfg, nil
}
