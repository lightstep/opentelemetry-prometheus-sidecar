module github.com/lightstep/opentelemetry-prometheus-sidecar

replace github.com/prometheus/prometheus => ../../prometheus/prometheus

require (
	contrib.go.opencensus.io/exporter/ocagent v0.4.12 // indirect
	contrib.go.opencensus.io/exporter/prometheus v0.1.0
	github.com/OneOfOne/xxhash v1.2.5 // indirect
	github.com/StackExchange/wmi v0.0.0-20180725035823-b12b22c5341f // indirect
	github.com/VividCortex/ewma v1.1.1 // indirect
	github.com/biogo/store v0.0.0-20160505134755-913427a1d5e8 // indirect
	github.com/cenk/backoff v2.0.0+incompatible // indirect
	github.com/certifi/gocertifi v0.0.0-20180905225744-ee1a9a0726d2 // indirect
	github.com/cockroachdb/apd v1.1.0 // indirect
	github.com/cockroachdb/cmux v0.0.0-20170110192607-30d10be49292 // indirect
	github.com/cockroachdb/cockroach v0.0.0-20170608034007-84bc9597164f // indirect
	github.com/cockroachdb/cockroach-go v0.0.0-20181001143604-e0a95dfd547c // indirect
	github.com/coreos/etcd v3.3.12+incompatible // indirect
	github.com/d4l3k/messagediff v1.2.1 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dustin/go-humanize v1.0.0 // indirect
	github.com/elastic/gosigar v0.9.0 // indirect
	github.com/elazarl/go-bindata-assetfs v1.0.0 // indirect
	github.com/facebookgo/clock v0.0.0-20150410010913-600d898af40a // indirect
	github.com/getsentry/raven-go v0.1.2 // indirect
	github.com/ghodss/yaml v1.0.0
	github.com/go-ini/ini v1.25.4 // indirect
	github.com/go-kit/kit v0.10.0
	github.com/go-ole/go-ole v1.2.4 // indirect
	github.com/gogo/protobuf v1.3.1
	github.com/golang/protobuf v1.4.3
	github.com/google/go-cmp v0.5.2
	github.com/grpc-ecosystem/go-grpc-prometheus v1.2.0
	github.com/grpc-ecosystem/grpc-opentracing v0.0.0-20180507213350-8e809c8a8645 // indirect
	github.com/hashicorp/go-msgpack v0.5.4 // indirect
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
	github.com/prometheus/client_golang v1.8.0
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
	github.com/rlmcpherson/s3gof3r v0.5.0 // indirect
	github.com/rubyist/circuitbreaker v2.2.1+incompatible // indirect
	github.com/sasha-s/go-deadlock v0.0.0-20161201235124-341000892f3d // indirect
	github.com/shopspring/decimal v0.0.0-20180709203117-cd690d0c9e24 // indirect
	github.com/stretchr/testify v1.5.1 // indirect
	go.opencensus.io v0.22.4
	golang.org/x/time v0.0.0-20200630173020-3af7569d3a1e
	golang.org/x/tools v0.0.0-20201014170642-d1624618ad65 // indirect
	google.golang.org/genproto v0.0.0-20200904004341-0bd0a958aa1d
	google.golang.org/grpc v1.32.0
	gopkg.in/alecthomas/kingpin.v2 v2.2.6
	gopkg.in/d4l3k/messagediff.v1 v1.2.1
	honnef.co/go/tools v0.0.1-2020.1.6 // indirect
	k8s.io/client-go v11.0.1-0.20190409021438-1a26190bd76a+incompatible // indirect
)

go 1.14
