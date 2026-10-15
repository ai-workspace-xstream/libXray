// Package telemetry owns the opt-in libXray trace provider.
// Call Configure only after the user enables network diagnostics.
package telemetry

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv/v1.37.0"
)

const endpointHost = "observability.svc.plus"

var providerMu sync.Mutex
var provider *sdktrace.TracerProvider

// Configure enables bounded OTLP export with an in-memory, short-lived token.
// The endpoint is pinned to the existing XConnect observability service.
func Configure(endpoint, ingestToken, serviceVersion string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), endpointHost) || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("telemetry endpoint must use HTTPS on observability.svc.plus")
	}
	if parsed.Path != "/otlp" && parsed.Path != "/otlp/v1/traces" {
		return errors.New("telemetry endpoint path must be /otlp or /otlp/v1/traces")
	}
	if parsed.Path == "/otlp" {
		parsed.Path = "/otlp/v1/traces"
		endpoint = parsed.String()
	}
	if strings.TrimSpace(ingestToken) == "" {
		return errors.New("diagnostic ingest token is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(endpoint),
		otlptracehttp.WithHeaders(map[string]string{"Authorization": "Bearer " + ingestToken}),
		otlptracehttp.WithTimeout(3*time.Second),
	)
	if err != nil {
		return err
	}
	attributes := []attribute.KeyValue{semconv.ServiceName("xconnect-libxray")}
	if serviceVersion != "" {
		attributes = append(attributes, semconv.ServiceVersion(serviceVersion))
	}
	res, err := resource.New(ctx, resource.WithTelemetrySDK(), resource.WithAttributes(attributes...))
	if err != nil {
		_ = exporter.Shutdown(ctx)
		return err
	}
	next := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exporter,
			sdktrace.WithMaxQueueSize(512),
			sdktrace.WithBatchTimeout(5*time.Second),
			sdktrace.WithExportTimeout(3*time.Second),
		),
	)
	providerMu.Lock()
	previous := provider
	provider = next
	otel.SetTracerProvider(next)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	providerMu.Unlock()
	if previous != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		return previous.Shutdown(shutdownCtx)
	}
	return nil
}

// Shutdown stops export and drops the provider's reference to auth material.
func Shutdown(ctx context.Context) error {
	providerMu.Lock()
	previous := provider
	provider = nil
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	providerMu.Unlock()
	if previous == nil {
		return nil
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
	}
	return previous.Shutdown(ctx)
}
