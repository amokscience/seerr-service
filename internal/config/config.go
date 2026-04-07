package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	// AWS / SQS
	AWSRegion          string
	SQSQueueURL        string
	SQSMaxMessages     int32
	SQSVisibilityTimeout int32
	SQSWaitTimeSeconds int32
	SQSPollingInterval time.Duration

	// Seerr (Overseerr / Jellyseerr)
	SeerrBaseURL string
	SeerrAPIKey  string

	// Service
	LogLevel    string
	HealthPort  string
}

// Load reads configuration from environment variables and validates required fields.
func Load() (*Config, error) {
	cfg := &Config{
		AWSRegion:            getEnv("AWS_REGION", "us-east-1"),
		SQSQueueURL:          getEnv("SQS_QUEUE_URL", ""),
		SQSMaxMessages:       int32(getEnvInt("SQS_MAX_MESSAGES", 10)),
		SQSVisibilityTimeout: int32(getEnvInt("SQS_VISIBILITY_TIMEOUT", 30)),
		SQSWaitTimeSeconds:   int32(getEnvInt("SQS_WAIT_TIME_SECONDS", 20)),
		SQSPollingInterval:   time.Duration(getEnvInt("SQS_POLLING_INTERVAL_MS", 500)) * time.Millisecond,

		SeerrBaseURL: getEnv("SEERR_BASE_URL", ""),
		SeerrAPIKey:  getEnv("SEERR_API_KEY", ""),

		LogLevel:   getEnv("LOG_LEVEL", "info"),
		HealthPort: getEnv("HEALTH_PORT", "8080"),
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	if c.SQSQueueURL == "" {
		return fmt.Errorf("SQS_QUEUE_URL is required")
	}
	if c.SeerrBaseURL == "" {
		return fmt.Errorf("SEERR_BASE_URL is required")
	}
	if c.SeerrAPIKey == "" {
		return fmt.Errorf("SEERR_API_KEY is required")
	}
	return nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
