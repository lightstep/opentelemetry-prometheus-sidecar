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
	contrib.go.opencensus.io/exporter/ocagent v0.4.12 // indirect
	github.com/VividCortex/ewma v1.1.1 // indirect
	github.com/biogo/store v0.0.0-20160505134755-913427a1d5e8 // indirect
	github.com/cenk/backoff v2.0.0+incompatible // indirect
	github.com/certifi/gocertifi v0.0.0-20180905225744-ee1a9a0726d2 // indirect
	github.com/cockroachdb/apd v1.1.0 // indirect
	github.com/cockroachdb/cmux v0.0.0-20170110192607-30d10be49292 // indirect
	github.com/cockroachdb/cockroach v0.0.0-20170608034007-84bc9597164f // indirect
	github.com/cockroachdb/cockroach-go v0.0.0-20181001143604-e0a95dfd547c // indirect
	github.com/d4l3k/messagediff v1.2.1 // indirect
	github.com/dustin/go-humanize v1.0.0 // indirect
	github.com/elastic/gosigar v0.9.0 // indirect
	github.com/elazarl/go-bindata-assetfs v1.0.0 // indirect
	github.com/facebookgo/clock v0.0.0-20150410010913-600d898af40a // indirect
	github.com/getsentry/raven-go v0.1.2 // indirect
	github.com/ghodss/yaml v1.0.0
	github.com/go-ini/ini v1.25.4 // indirect
	github.com/go-kit/kit v0.10.0
	github.com/gogo/protobuf v1.3.1
	github.com/golang/protobuf v1.4.2
	github.com/google/go-cmp v0.5.2
	github.com/grpc-ecosystem/go-grpc-prometheus v1.2.0
	github.com/jackc/fake v0.0.0-20150926172116-812a484cc733 // indirect
	github.com/jackc/pgx v3.2.0+incompatible // indirect
	github.com/jpillora/backoff v1.0.0 // indirect
	github.com/knz/strtime v0.0.0-20181018220328-af2256ee352c // indirect
	github.com/mattn/go-runewidth v0.0.4 // indirect
	github.com/mitchellh/reflectwalk v1.0.1 // indirect
	github.com/montanaflynn/stats v0.0.0-20180911141734-db72e6cae808 // indirect
	github.com/mwitkow/go-conntrack v0.0.0-20190716064945-2f068394615f
	github.com/oklog/oklog v0.3.2
	github.com/olekukonko/tablewriter v0.0.1 // indirect
	github.com/peterbourgon/g2s v0.0.0-20170223122336-d4e7ad98afea // indirect
	github.com/petermattis/goid v0.0.0-20170504144140-0ded85884ba5 // indirect
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.7.1
	github.com/prometheus/common v0.14.0
	github.com/prometheus/prometheus v1.8.2-0.20200827201422-1195cc24e3c8
	github.com/prometheus/tsdb v0.10.0
	github.com/rlmcpherson/s3gof3r v0.5.0 // indirect
	github.com/rubyist/circuitbreaker v2.2.1+incompatible // indirect
	github.com/sasha-s/go-deadlock v0.0.0-20161201235124-341000892f3d // indirect
	github.com/sethvargo/go-envconfig v0.3.2
	github.com/shopspring/decimal v0.0.0-20180709203117-cd690d0c9e24 // indirect
	github.com/stretchr/testify v1.6.1
	go.opentelemetry.io/collector v0.13.0
	go.opentelemetry.io/contrib/instrumentation/host v0.13.0
	go.opentelemetry.io/contrib/instrumentation/runtime v0.13.0
	go.opentelemetry.io/contrib/propagators v0.13.0
	go.opentelemetry.io/otel v0.13.0
	go.opentelemetry.io/otel/exporters/otlp v0.13.0
	go.opentelemetry.io/otel/sdk v0.13.0
	golang.org/x/net v0.0.0-20200822124328-c89045814202 // indirect
	golang.org/x/sync v0.0.0-20200625203802-6e8e738ad208 // indirect
	golang.org/x/time v0.0.0-20200630173020-3af7569d3a1e
	google.golang.org/genproto v0.0.0-20200815001618-f69a88009b70
	google.golang.org/grpc v1.32.0
	gopkg.in/alecthomas/kingpin.v2 v2.2.6
	gopkg.in/d4l3k/messagediff.v1 v1.2.1
	k8s.io/client-go v11.0.1-0.20190409021438-1a26190bd76a+incompatible // indirect
)

go 1.14
