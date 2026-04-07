// Package telemetry sets up OpenTelemetry tracing for the service.
// It configures an OTLP gRPC exporter when OTEL_EXPORTER_OTLP_ENDPOINT is set,
// and falls back to a no-op provider when telemetry is disabled.
package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const instrumentationName = "github.com/amokscience/seerr-service"

// ShutdownFunc must be called on service exit to flush and close the exporter.
type ShutdownFunc func(ctx context.Context) error

// Setup initialises the global OpenTelemetry tracer provider.
//
//   - When otlpEndpoint is non-empty, traces are exported via OTLP gRPC.
//   - When otlpEndpoint is empty (or enabled=false), a no-op provider is
//     installed so instrumentation calls are safe but produce nothing.
//
// Returns a ShutdownFunc that must be deferred in main.
func Setup(ctx context.Context, serviceName, otlpEndpoint string, enabled bool, log *slog.Logger) (ShutdownFunc, error) {
	if !enabled || otlpEndpoint == "" {
		log.Info("opentelemetry tracing disabled (no OTEL_EXPORTER_OTLP_ENDPOINT set)")
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(_ context.Context) error { return nil }, nil
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("build otel resource: %w", err)
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(otlpEndpoint),
		otlptracegrpc.WithInsecure(), // TODO: switch to TLS in production
	)
	if err != nil {
		return nil, fmt.Errorf("create otlp exporter: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()), // TODO: adjust sampling rate for prod
	)

	// Set as globals so otel.Tracer() works anywhere in the codebase.
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	log.Info("opentelemetry tracing enabled",
		slog.String("endpoint", otlpEndpoint),
		slog.String("serviceName", serviceName),
	)

	shutdown := func(ctx context.Context) error {
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := tp.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("otel shutdown: %w", err)
		}
		return nil
	}

	return shutdown, nil
}

// Tracer returns a named tracer from the global provider.
func Tracer() trace.Tracer {
	return otel.Tracer(instrumentationName)
}
