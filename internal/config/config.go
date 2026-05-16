package config

import (
	"os"

	"gopkg.in/yaml.v3"
	"smart-router/internal/types"
)

type PlansConfig struct {
	Plans map[string]types.PlanConfig `yaml:"plans"`
}

func LoadFromFile(path string) (*PlansConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg PlansConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
