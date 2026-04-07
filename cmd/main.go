package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/config"

	svcconfig "github.com/amokscience/seerr-service/internal/config"
	"github.com/amokscience/seerr-service/internal/processor"
	"github.com/amokscience/seerr-service/internal/pushover"
	"github.com/amokscience/seerr-service/internal/seerr"
	sqsconsumer "github.com/amokscience/seerr-service/internal/sqsconsumer"
	"github.com/amokscience/seerr-service/internal/telemetry"
)

func main() {
	// -------------------------------------------------------------------------
	// Logger  (JSON + trace context bridge)
	// -------------------------------------------------------------------------
	baseHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	log := slog.New(telemetry.NewTraceHandler(baseHandler))
	slog.SetDefault(log)

	// -------------------------------------------------------------------------
	// Configuration
	// -------------------------------------------------------------------------
	cfg, err := svcconfig.Load(log)
	if err != nil {
		log.Error("failed to load configuration", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// -------------------------------------------------------------------------
	// OpenTelemetry
	// -------------------------------------------------------------------------
	otelShutdown, err := telemetry.Setup(context.Background(),
		cfg.OTelServiceName,
		cfg.OTelEndpoint,
		cfg.OTelEnabled,
		log,
	)
	if err != nil {
		log.Error("failed to initialise opentelemetry", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer func() {
		if err := otelShutdown(context.Background()); err != nil {
			log.Error("opentelemetry shutdown error", slog.String("error", err.Error()))
		}
	}()

	// -------------------------------------------------------------------------
	// AWS SDK
	// -------------------------------------------------------------------------
	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(cfg.AWSRegion),
	)
	if err != nil {
		log.Error("failed to load AWS config", slog.String("error", err.Error()))
		os.Exit(1)
	}

	// -------------------------------------------------------------------------
	// Dependencies
	// -------------------------------------------------------------------------
	seerrClient := seerr.New(cfg.SeerrBaseURL, cfg.SeerrAPIKey, log)
	pushoverClient := pushover.New(cfg.PushoverWebhookURL, cfg.PushoverToken, log)
	pushoverClient.Notify(context.Background(), "seerr-service starting", "Connected and polling SQS queue.")
	proc := processor.New(seerrClient, pushoverClient, log)
	consumer := sqsconsumer.New(awsCfg, cfg, log)

	// -------------------------------------------------------------------------
	// Health / readiness probe  (Kubernetes liveness & readiness)
	// -------------------------------------------------------------------------
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		// TODO: add deeper readiness checks (e.g. Seerr reachability) if desired
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	healthSrv := &http.Server{
		Addr:    ":" + cfg.HealthPort,
		Handler: mux,
	}

	go func() {
		log.Info("health server listening", slog.String("addr", healthSrv.Addr))
		if err := healthSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("health server error", slog.String("error", err.Error()))
		}
	}()

	// -------------------------------------------------------------------------
	// Graceful shutdown context
	// -------------------------------------------------------------------------
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// -------------------------------------------------------------------------
	// SQS consumer loop
	// -------------------------------------------------------------------------
	log.Info("seerr-service started")
	if err := consumer.Run(ctx, proc.Process); err != nil && err != context.Canceled {
		log.Error("consumer exited with error", slog.String("error", err.Error()))
		os.Exit(1)
	}

	_ = healthSrv.Shutdown(context.Background())
	log.Info("seerr-service stopped")
}
