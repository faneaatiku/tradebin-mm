package config

import (
	"fmt"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
	"os"
)

const (
	defaultOrdersNumber = 10
)

// LoadConfig reads a file, validates its existence, and parses it into the provided structure
func LoadConfig(filePath string) (*Config, error) {
	// Check if the file exists
	if _, err := os.Stat(filePath); err != nil {
		if os.IsNotExist(err) {

			return nil, fmt.Errorf("file does not exist: %s", filePath)
		}

		return nil, fmt.Errorf("error checking file: %v", err)
	}

	yamlFile, err := os.ReadFile(filePath)
	if err != nil {

		return nil, fmt.Errorf("failed to read config.yaml: %w", err)
	}

	// Decode YAML content into the structure
	var result Config
	err = yaml.Unmarshal(yamlFile, &result)
	if err != nil {

		return nil, fmt.Errorf("error decoding YAML file: %v", err)
	}

	addDefaults(&result)

	return &result, nil
}

func addDefaults(cfg *Config) {
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = logrus.InfoLevel.String()
	}

	if cfg.Orders.Buy == 0 {
		cfg.Orders.Buy = defaultOrdersNumber
	}

	if cfg.Orders.Sell == 0 {
		cfg.Orders.Sell = defaultOrdersNumber
	}
}
