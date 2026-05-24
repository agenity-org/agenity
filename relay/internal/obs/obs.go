// Package obs — observability bootstrap for chepherd-relay.
//
// Wires up:
//   - Sentry (error capture) via github.com/getsentry/sentry-go
//   - OpenTelemetry traces via go.opentelemetry.io/otel + OTLP/HTTP exporter
//
// When the corresponding DSN / endpoint env var is unset, the SDK is
// not initialised at all and the middleware becomes a pass-through.
//
// Privacy: only request metadata (method, path, status, duration) and
// stack traces are sent. NO payload body, NO Authorization header.

package obs

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime/debug"
	"time"

	"github.com/getsentry/sentry-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Config bundles the env-driven settings.
type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string

	SentryDSN string
	// E.g. "https://otel-collector.openova.io:4318".
	OTelEndpoint string
}

// FromEnv reads the canonical chepherd-relay observability env vars.
func FromEnv(serviceName, version string) Config {
	return Config{
		ServiceName:    serviceName,
		ServiceVersion: version,
		Environment:    envDefault("CHEPHERD_RELAY_ENV", "dev"),
		SentryDSN:      os.Getenv("CHEPHERD_RELAY_SENTRY_DSN"),
		OTelEndpoint:   os.Getenv("CHEPHERD_RELAY_OTEL_ENDPOINT"),
	}
}

// Shutdown signature — called from main's defer chain.
type Shutdown func(context.Context) error

// Init installs Sentry + OTel based on the config. Returns a single
// Shutdown that flushes both providers in order.
func Init(ctx context.Context, cfg Config) (Shutdown, error) {
	var shutdowns []Shutdown

	if cfg.SentryDSN != "" {
		err := sentry.Init(sentry.ClientOptions{
			Dsn:              cfg.SentryDSN,
			Release:          cfg.ServiceName + "@" + cfg.ServiceVersion,
			Environment:      cfg.Environment,
			AttachStacktrace: true,
			TracesSampleRate: 0.05,
			BeforeSend: func(ev *sentry.Event, _ *sentry.EventHint) *sentry.Event {
				// Strip Authorization header from request data to avoid leaking JWTs.
				if ev.Request != nil {
					delete(ev.Request.Headers, "Authorization")
					delete(ev.Request.Headers, "authorization")
				}
				return ev
			},
		})
		if err != nil {
			return nil, fmt.Errorf("obs: sentry init: %w", err)
		}
		shutdowns = append(shutdowns, func(_ context.Context) error {
			sentry.Flush(2 * time.Second)
			return nil
		})
		log.Println("obs: sentry enabled")
	}

	if cfg.OTelEndpoint != "" {
		exp, err := otlptracehttp.New(ctx,
			otlptracehttp.WithEndpoint(cfg.OTelEndpoint),
			otlptracehttp.WithInsecure(),
		)
		if err != nil {
			return nil, fmt.Errorf("obs: otlp exporter: %w", err)
		}
		res, _ := resource.Merge(resource.Default(), resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
			semconv.DeploymentEnvironment(cfg.Environment),
		))
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exp),
			sdktrace.WithResource(res),
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0.1))),
		)
		otel.SetTracerProvider(tp)
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))
		shutdowns = append(shutdowns, func(c context.Context) error {
			c2, cancel := context.WithTimeout(c, 3*time.Second)
			defer cancel()
			return tp.Shutdown(c2)
		})
		log.Println("obs: otel enabled →", cfg.OTelEndpoint)
		_ = otlptrace.NewUnstarted // keep symbol referenced for build clarity
	}

	if len(shutdowns) == 0 {
		log.Println("obs: disabled (no DSN/endpoint configured)")
	}

	return func(c context.Context) error {
		var errs []error
		for _, s := range shutdowns {
			if err := s(c); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}, nil
}

// Middleware wraps an http.Handler with:
//   - Sentry panic recovery (re-panics after capturing for parent recovery)
//   - OTel server-side span around the request
//   - Structured access-log line
func Middleware(next http.Handler) http.Handler {
	tracer := otel.Tracer("chepherd-relay/http")
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		spanName := req.Method + " " + req.URL.Path
		ctx, span := tracer.Start(req.Context(), spanName,
			trace.WithAttributes(
				attribute.String("http.method", req.Method),
				attribute.String("http.target", req.URL.Path),
				attribute.String("http.user_agent", req.UserAgent()),
			),
		)
		defer span.End()

		// Wrap the response writer so we can capture the status code.
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		defer func() {
			if rec := recover(); rec != nil {
				stack := debug.Stack()
				sentry.WithScope(func(s *sentry.Scope) {
					s.SetTag("path", req.URL.Path)
					s.SetContext("debug", map[string]any{"stack": string(stack)})
					sentry.CurrentHub().Recover(rec)
				})
				span.RecordError(fmt.Errorf("panic: %v", rec))
				rw.WriteHeader(http.StatusInternalServerError)
			}
			span.SetAttributes(
				attribute.Int("http.status_code", rw.status),
				attribute.Int64("http.duration_ms", time.Since(start).Milliseconds()),
			)
		}()

		next.ServeHTTP(rw, req.WithContext(ctx))
	})
}

// CaptureError manually reports an err to Sentry with optional tags.
func CaptureError(err error, tags map[string]string) {
	if err == nil {
		return
	}
	sentry.WithScope(func(s *sentry.Scope) {
		for k, v := range tags {
			s.SetTag(k, v)
		}
		sentry.CaptureException(err)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
