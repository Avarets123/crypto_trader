package config

import (
	"os"
)

type Config struct {
	DatabaseDSN string
	LogLevel    string
}

// Load reads configuration from environment variables.
func Load() (*Config, error) {
	dsn := os.Getenv("DATABASE_URL")
	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "debug"
	}
	return &Config{
		DatabaseDSN: dsn,
		LogLevel:    logLevel,
	}, nil
}
