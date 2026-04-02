package config

import (
	"os"
)

type Config struct {
	DatabaseDSN string
	HTTPAddr    string
	LogLevel    string
}

// Load reads configuration from environment variables.
func Load() (*Config, error) {
	dsn := os.Getenv("DATABASE_URL")
	// if dsn == "" {
	// 	return nil, fmt.Errorf("config: DATABASE_URL is required")
	// }
	addr := os.Getenv("HTTP_ADDR")
	// if addr == "" {
	// 	addr = ":8080"
	// }
	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "" {
		logLevel = "debug"
	}
	return &Config{
		DatabaseDSN: dsn,
		HTTPAddr:    addr,
		LogLevel:    logLevel,
	}, nil
}
