module github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry

go 1.15

require (
	github.com/go-kit/kit v0.10.0
	github.com/go-logfmt/logfmt v0.5.0
	github.com/pkg/errors v0.9.1
	github.com/stretchr/testify v1.6.1
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.16.0
	go.opentelemetry.io/contrib/instrumentation/host v0.16.0
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.16.0
	go.opentelemetry.io/contrib/instrumentation/runtime v0.16.0
	go.opentelemetry.io/contrib/propagators v0.16.0
	go.opentelemetry.io/otel v0.16.0
	go.opentelemetry.io/otel/exporters/otlp v0.16.0
	go.opentelemetry.io/otel/sdk v0.16.0
	google.golang.org/grpc v1.34.0
)
