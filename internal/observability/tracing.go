package observability

import (
	"context"
	"errors"
	"net/url"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.30.0"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/zhangzhe-ctrl/ani-session-gateway"

type TracingConfig struct{ ServiceName, ExporterEndpoint string }

type Tracing struct {
	provider    *sdktrace.TracerProvider
	once        sync.Once
	shutdownErr error
}

func InitTracing(ctx context.Context, config TracingConfig) (*Tracing, error) {
	if config.ServiceName == "" {
		return nil, errors.New("OpenTelemetry service name is required")
	}
	options := []sdktrace.TracerProviderOption{sdktrace.WithResource(resource.NewSchemaless(semconv.ServiceName(config.ServiceName)))}
	if config.ExporterEndpoint != "" {
		u, err := url.Parse(config.ExporterEndpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("invalid OTLP exporter endpoint")
		}
		exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(config.ExporterEndpoint))
		if err != nil {
			return nil, err
		}
		options = append(options, sdktrace.WithBatcher(exporter))
	}
	provider := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(provider)
	return &Tracing{provider: provider}, nil
}

func (t *Tracing) Shutdown(ctx context.Context) error {
	if t == nil || t.provider == nil {
		return nil
	}
	t.once.Do(func() { t.shutdownErr = t.provider.Shutdown(ctx) })
	return t.shutdownErr
}

func StartSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return otel.Tracer(instrumentationName).Start(ctx, name)
}
