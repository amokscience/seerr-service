package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	// AWS credentials – loaded into env so the AWS SDK picks them up automatically
	// via its standard credential chain (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY).
	AWSAccessKeyID     string
	AWSSecretAccessKey string

	// AWS / SQS
	AWSRegion            string
	SQSQueueURL          string
	SQSMaxMessages       int32
	SQSVisibilityTimeout int32
	SQSWaitTimeSeconds   int32
	SQSPollingInterval   time.Duration

	// Seerr (Overseerr / Jellyseerr)
	SeerrBaseURL string
	SeerrAPIKey  string

	// Service
	LogLevel   string
	HealthPort string
}

// Load resolves configuration using the following priority (highest → lowest):
//  1. OS environment variables
//  2. .env file in the working directory (ignored if not present)
//
// All required fields must be set by one of the above sources; every missing
// field is logged before returning a combined error.
func Load(log *slog.Logger) (*Config, error) {
	loadDotEnv(log)

	cfg := &Config{
		AWSAccessKeyID:     getEnv("AWS_ACCESS_KEY_ID", ""),
		AWSSecretAccessKey: getEnv("AWS_SECRET_ACCESS_KEY", ""),

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

	if err := cfg.validate(log); err != nil {
		return nil, err
	}

	return cfg, nil
}

// required pairs a human-readable name with the resolved value.
type required struct {
	envVar string
	value  string
}

// validate checks all required fields, logs each missing one, then returns a
// combined error so the operator sees every problem at once.
func (c *Config) validate(log *slog.Logger) error {
	fields := []required{
		{"AWS_ACCESS_KEY_ID", c.AWSAccessKeyID},
		{"AWS_SECRET_ACCESS_KEY", c.AWSSecretAccessKey},
		{"AWS_REGION", c.AWSRegion},
		{"SQS_QUEUE_URL", c.SQSQueueURL},
		{"SEERR_BASE_URL", c.SeerrBaseURL},
		{"SEERR_API_KEY", c.SeerrAPIKey},
	}

	var errs []error
	for _, f := range fields {
		if f.value == "" {
			log.Error("required configuration value is not set",
				slog.String("envVar", f.envVar),
				slog.String("hint", "set it as an environment variable or add it to the .env file"),
			)
			errs = append(errs, fmt.Errorf("%s is required but not set", f.envVar))
		}
	}

	return errors.Join(errs...)
}

// loadDotEnv attempts to load a .env file from the working directory.
// It only sets variables that are NOT already present in the environment,
// so real environment variables always take precedence.
// Missing .env file is not an error – the service may rely purely on env vars.
func loadDotEnv(log *slog.Logger) {
	err := godotenv.Load(".env")
	if err != nil {
		if os.IsNotExist(err) {
			log.Debug(".env file not found, relying on environment variables only")
		} else {
			log.Warn("failed to parse .env file", slog.String("error", err.Error()))
		}
	} else {
		log.Info("loaded configuration from .env file")
	}
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
