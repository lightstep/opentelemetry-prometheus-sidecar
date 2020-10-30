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
	cloud.google.com/go v0.49.0 // indirect
	github.com/aws/aws-sdk-go v1.23.20 // indirect
	github.com/d4l3k/messagediff v1.2.1 // indirect
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
	github.com/stretchr/testify v1.4.0 // indirect
	golang.org/x/net v0.0.0-20200822124328-c89045814202 // indirect
	golang.org/x/sync v0.0.0-20200625203802-6e8e738ad208 // indirect
	golang.org/x/time v0.0.0-20191024005414-555d28b269f0
	google.golang.org/appengine v1.6.2 // indirect
	google.golang.org/genproto v0.0.0-20191115221424-83cc0476cb11
	google.golang.org/grpc v1.25.1
	gopkg.in/alecthomas/kingpin.v2 v2.2.6
	gopkg.in/d4l3k/messagediff.v1 v1.2.1
)

go 1.14
