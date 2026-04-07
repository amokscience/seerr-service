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
	"github.com/amokscience/seerr-service/internal/seerr"
	sqsconsumer "github.com/amokscience/seerr-service/internal/sqsconsumer"
)

func main() {
	// -------------------------------------------------------------------------
	// Logger
	// -------------------------------------------------------------------------
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
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
	proc := processor.New(seerrClient, log)
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
