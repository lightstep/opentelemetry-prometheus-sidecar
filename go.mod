module github.com/lightstep/opentelemetry-prometheus-sidecar

require (
	github.com/d4l3k/messagediff v1.2.1 // indirect
	github.com/ghodss/yaml v1.0.0
	github.com/go-kit/kit v0.10.0
	github.com/go-logfmt/logfmt v0.5.0
	github.com/gogo/protobuf v1.3.1
	github.com/golang/protobuf v1.4.2
	github.com/google/go-cmp v0.5.2
	github.com/mwitkow/go-conntrack v0.0.0-20190716064945-2f068394615f
	github.com/oklog/oklog v0.3.2
	github.com/pkg/errors v0.9.1
	github.com/prometheus/common v0.14.0
	// Prometheus server does not follow go modules conventions:
	//
	// Release v2.22.0 / 2020-10-15 has git-sha 0a7fdd3b76960808c3a91d92267c3d815c1bc354
	//
	// Maps to:
	//
	//   github.com/prometheus/prometheus v1.8.2-0.20201015110737-0a7fdd3b7696
	//
	// Computed using:
	//
	//   go get github.com/prometheus/prometheus@0a7fdd3b76960808c3a91d92267c3d815c1bc354
	//
	// see https://github.com/prometheus/prometheus/issues/7663.
	github.com/prometheus/prometheus v1.8.2-0.20201015110737-0a7fdd3b7696
	github.com/stretchr/testify v1.6.1
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.13.0
	go.opentelemetry.io/contrib/instrumentation/host v0.13.0
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.13.0
	go.opentelemetry.io/contrib/instrumentation/runtime v0.13.0
	go.opentelemetry.io/contrib/propagators v0.13.0
	go.opentelemetry.io/otel v0.13.0
	go.opentelemetry.io/otel/exporters/otlp v0.13.0
	go.opentelemetry.io/otel/sdk v0.13.0
	golang.org/x/time v0.0.0-20200630173020-3af7569d3a1e
	golang.org/x/tools v0.0.0-20201014170642-d1624618ad65 // indirect
	google.golang.org/genproto v0.0.0-20200904004341-0bd0a958aa1d
	google.golang.org/grpc v1.32.0
	gopkg.in/alecthomas/kingpin.v2 v2.2.6
	gopkg.in/d4l3k/messagediff.v1 v1.2.1
)

go 1.14
