package observability_test

import (
	"context"
	"strings"
	"testing"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/observability"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTracingInitAndIdempotentShutdown(t *testing.T) {
	if _, err := observability.InitTracing(context.Background(), observability.TracingConfig{}); err == nil {
		t.Fatal("empty service name accepted")
	}
	if _, err := observability.InitTracing(context.Background(), observability.TracingConfig{ServiceName: "test", ExporterEndpoint: "collector:4317?ticket=secret"}); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
	tracing, err := observability.InitTracing(context.Background(), observability.TracingConfig{ServiceName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tracing.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := tracing.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSpansExcludeSensitiveValues(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	secret := "one-time-ticket-and-terminal-payload"
	ctx := context.WithValue(context.Background(), struct{}{}, secret)
	_, span := observability.StartSpan(ctx, "session_store.claim_and_reserve")
	span.End()
	for _, recorded := range recorder.Ended() {
		if strings.Contains(recorded.Name(), secret) {
			t.Fatal("span name contains sensitive value")
		}
		for _, attr := range recorded.Attributes() {
			if strings.Contains(attr.Value.AsString(), secret) {
				t.Fatalf("span attribute contains sensitive value: %v", attr)
			}
		}
		for _, event := range recorded.Events() {
			if strings.Contains(event.Name, secret) {
				t.Fatal("span event contains sensitive value")
			}
		}
	}
}
