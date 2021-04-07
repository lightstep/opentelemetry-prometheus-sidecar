module github.com/lightstep/opentelemetry-prometheus-sidecar

require (
	github.com/d4l3k/messagediff v1.2.1 // indirect
	github.com/ghodss/yaml v1.0.0
	github.com/go-kit/kit v0.10.0
	github.com/go-logfmt/logfmt v0.5.0
	github.com/golang/protobuf v1.4.3
	github.com/golang/snappy v0.0.2
	github.com/google/go-cmp v0.5.4
	github.com/google/uuid v1.1.2
	github.com/hashicorp/go-version v1.2.0
	github.com/oklog/run v1.1.0
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_model v0.2.0
	github.com/prometheus/common v0.15.0
	github.com/prometheus/prom2json v1.3.0
	// Prometheus server does not follow go modules conventions:
	//
	// Release v2.24.1 / 2021-01-19 has git-sha 0a7fdd3b76960808c3a91d92267c3d815c1bc354
	//
	// Maps to:
	//
	//   github.com/prometheus/prometheus v1.8.2-0.20210119214810-e4487274853c
	//
	// Computed using:
	//
	//   go get github.com/prometheus/prometheus@e4487274853c587717006eeda8804e597d120340
	//
	// see https://github.com/prometheus/prometheus/issues/7663.
	github.com/prometheus/prometheus v1.8.2-0.20210119214810-e4487274853c
	github.com/stretchr/testify v1.7.0
	go.opentelemetry.io/contrib/instrumentation/host v0.18.0
	go.opentelemetry.io/contrib/instrumentation/runtime v0.18.0
	go.opentelemetry.io/contrib/propagators v0.18.0
	go.opentelemetry.io/otel v0.18.0
	go.opentelemetry.io/otel/exporters/otlp v0.18.0
	go.opentelemetry.io/otel/metric v0.18.0
	go.opentelemetry.io/otel/sdk v0.18.0
	go.opentelemetry.io/otel/sdk/export/metric v0.18.0
	go.opentelemetry.io/otel/sdk/metric v0.18.0
	go.opentelemetry.io/otel/trace v0.18.0
	go.opentelemetry.io/proto/otlp v0.7.0
	google.golang.org/genproto v0.0.0-20201201144952-b05cb90ed32e
	google.golang.org/grpc v1.36.0
	google.golang.org/protobuf v1.25.0
	gopkg.in/alecthomas/kingpin.v2 v2.2.6
	gopkg.in/d4l3k/messagediff.v1 v1.2.1
	gopkg.in/yaml.v2 v2.4.0
)

go 1.15
