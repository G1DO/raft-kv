package main

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// setupTracing builds an OTLP/HTTP exporter pointed at endpoint (host:port,
// e.g. Tempo's built-in receiver on :4318 — no Collector in between, ADR-007)
// and returns the tracer plus a flush-and-stop func for shutdown. Tracing is
// strictly opt-in: callers only get here when --otlp-endpoint is set, so the
// zero-config binary never touches the network for telemetry.
func setupTracing(endpoint, nodeID string, logger *slog.Logger) (trace.Tracer, func(), error) {
	exporter, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, nil, err
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(sdkresource.NewSchemaless(
			attribute.String("service.name", "raft-kv"),
			attribute.String("service.instance.id", nodeID),
		)),
	)

	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			logger.Warn("tracer_shutdown_failed", "error", err)
		}
	}
	return provider.Tracer("raft-kv"), shutdown, nil
}
