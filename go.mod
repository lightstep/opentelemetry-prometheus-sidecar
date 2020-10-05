module github.com/lightstep/lightstep-prometheus-sidecar

require (
	cloud.google.com/go v0.49.0
	contrib.go.opencensus.io/exporter/prometheus v0.1.0
	contrib.go.opencensus.io/exporter/stackdriver v0.12.8
	github.com/ghodss/yaml v1.0.0
	github.com/go-kit/kit v0.9.0
	github.com/golang/protobuf v1.3.2
	github.com/google/go-cmp v0.3.1
	github.com/grpc-ecosystem/go-grpc-prometheus v1.2.0
	github.com/jpillora/backoff v1.0.0 // indirect
	github.com/mwitkow/go-conntrack v0.0.0-20190716064945-2f068394615f
	github.com/oklog/oklog v0.3.2
	github.com/pkg/errors v0.8.1
	github.com/prometheus/client_golang v1.0.0
	github.com/prometheus/common v0.4.1
	github.com/prometheus/prometheus v0.0.0-20190710134608-e5b22494857d
	github.com/prometheus/tsdb v0.10.0
	github.com/rakyll/statik v0.1.6
	go.opencensus.io v0.22.2
	golang.org/x/time v0.0.0-20191024005414-555d28b269f0
	google.golang.org/genproto v0.0.0-20191115221424-83cc0476cb11
	google.golang.org/grpc v1.25.1
	gopkg.in/alecthomas/kingpin.v2 v2.2.6
)

go 1.14
