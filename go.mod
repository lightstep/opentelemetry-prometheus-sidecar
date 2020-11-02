module github.com/lightstep/opentelemetry-prometheus-sidecar

// Note: we have trouble upgrading the prometheus dependencies here
// because v2.  The last version without a go.mod is 2.0.5, but the
// effort to update here is non-trivial.
//
// server response: not found:
// github.com/prometheus/prometheus@v2.21.0+incompatible: invalid
// version: +incompatible suffix not allowed: module contains a go.mod
// file, so semantic import versioning is required

require (
	contrib.go.opencensus.io/exporter/prometheus v0.1.0
	github.com/davecgh/go-spew v1.1.1
	github.com/ghodss/yaml v1.0.0
	github.com/go-kit/kit v0.10.0
	github.com/gogo/protobuf v1.3.1
	github.com/golang/protobuf v1.4.2
	github.com/google/go-cmp v0.5.2
	github.com/grpc-ecosystem/go-grpc-prometheus v1.2.0
	github.com/jpillora/backoff v1.0.0 // indirect
	github.com/mwitkow/go-conntrack v0.0.0-20190716064945-2f068394615f
	github.com/oklog/oklog v0.3.2
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.7.1
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
	github.com/stretchr/testify v1.5.1
	go.opencensus.io v0.22.4
	golang.org/x/time v0.0.0-20200630173020-3af7569d3a1e
	golang.org/x/tools v0.0.0-20201014170642-d1624618ad65 // indirect
	google.golang.org/genproto v0.0.0-20200904004341-0bd0a958aa1d
	google.golang.org/grpc v1.32.0
	gopkg.in/alecthomas/kingpin.v2 v2.2.6
	gopkg.in/d4l3k/messagediff.v1 v1.2.1
	honnef.co/go/tools v0.0.1-2020.1.6 // indirect
)

go 1.14
