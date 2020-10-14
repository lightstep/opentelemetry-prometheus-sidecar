module github.com/lightstep/lightstep-prometheus-sidecar

// Note: we have trouble upgrading the prometheus dependencies here
// because v2.  The last version without a go.mod is 2.0.5, but the
// effort to update here is non-trivial.
//
// server response: not found:
// github.com/prometheus/prometheus@v2.21.0+incompatible: invalid
// version: +incompatible suffix not allowed: module contains a go.mod
// file, so semantic import versioning is required

require (
	cloud.google.com/go v0.49.0 // indirect
	contrib.go.opencensus.io/exporter/prometheus v0.1.0
	github.com/aws/aws-sdk-go v1.23.20 // indirect
	github.com/ghodss/yaml v1.0.0
	github.com/go-kit/kit v0.9.0
	github.com/gogo/protobuf v1.3.1
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
	go.opencensus.io v0.22.2
	golang.org/x/time v0.0.0-20191024005414-555d28b269f0
	golang.org/x/tools v0.0.0-20201014170642-d1624618ad65 // indirect
	google.golang.org/appengine v1.6.2 // indirect
	google.golang.org/genproto v0.0.0-20191115221424-83cc0476cb11
	google.golang.org/grpc v1.25.1
	gopkg.in/alecthomas/kingpin.v2 v2.2.6
	honnef.co/go/tools v0.0.1-2020.1.6 // indirect
)

go 1.14
