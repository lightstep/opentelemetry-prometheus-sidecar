package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"testing"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/internal/otlptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/semconv"
	metricService "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	traceService "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	traces "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	grpcMetadata "google.golang.org/grpc/metadata"
	grpcmeta "google.golang.org/grpc/metadata"
	messagediff "gopkg.in/d4l3k/messagediff.v1"
)

// Ports used here:
// 19001: OTLP service
// 19002: Scrape target
// 19003: Scrape target with relabeling rules
// 19093: Prometheus

type (
	testServer struct {
		t        *testing.T
		stops    chan func()
		metrics  chan *metrics.ResourceMetrics
		trailers grpcmeta.MD
		metricService.UnimplementedMetricsServiceServer
	}

	traceServer struct {
		t     *testing.T
		stops chan func()
		spans chan *traces.ResourceSpans
		traceService.UnimplementedTraceServiceServer
	}
)

var (
	ErrUnsupported = fmt.Errorf("unsupported method")

	// e2eTestMainCommonFlags are needed to correctly call the
	// test gRPC server's Export().
	e2eTestMainSupervisorFlags = []string{
		"--security.root-certificate=testdata/certs/root_ca.crt",
		"--prometheus.endpoint=http://0.0.0.0:19093",
		"--destination.header",
		fmt.Sprint(e2eTestHeaderName, "=", e2eTestHeaderValue),
		"--diagnostics.header",
		fmt.Sprint(e2eTestHeaderName, "=", e2eTestHeaderValue),
		"--admin.port=9093",
	}

	e2eTestMainCommonFlags = append(e2eTestMainSupervisorFlags,
		"--disable-supervisor",
		"--disable-diagnostics",
		"--destination.endpoint=https://127.0.0.1:19001",
		"--diagnostics.endpoint=http://127.0.0.1:19000",
	)

	e2eReadyURL = "http://0.0.0.0:9093" + config.HealthCheckURI
)

const (
	e2eTestHeaderName   = "custom-header"
	e2eTestHeaderValue  = "Custom-Value"
	e2eMetricsPerScrape = 4
	e2eTestScrapes      = 5

	// largeQueue is set to be large enough to buffer all metric
	// points/spans exported during a single test.
	largeQueue = 10000

	e2eTestScrapeResultFmt = `
# HELP some_gauge Number of scrapes
# TYPE some_gauge gauge
some_gauge{kind="gauge"} %d

# HELP some_counter Number of scrapes
# TYPE some_counter counter
some_counter{kind="counter"} %d
`
	e2eTestScrapeResultFmtRelabeled = `
# HELP some_gauge_relabel Number of scrapes
# TYPE some_gauge_relabel gauge
some_gauge_relabel{kind="gauge"} %d

# HELP some_counter_relabel Number of scrapes
# TYPE some_counter_relabel counter
some_counter_relabel{kind="counter"} %d
`

	e2eTestPromConfig = `
global:
  scrape_interval: 1s

  external_labels:
    monitor: 'e2e-test'

scrape_configs:
  - job_name: 'test-target'

    static_configs:
    - targets: ['127.0.0.1:19002']
      labels:
        label1: 'L1'
        label2: 'L2'

  - job_name: 'test-relabeling'

    static_configs:
    - targets: ['127.0.0.1:19003']

    metric_relabel_configs:
    - source_labels: [instance]
      target_label: other_instance_id
    - action: labeldrop
      regex: (instance)
`
)

func TestE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	// Cancel-able context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create config file
	var err error
	var cfgDir, dataDir string

	if cfgDir, err = ioutil.TempDir("", "e2e-test-cfg"); err != nil {
		t.Fatal(err)
	}
	if dataDir, err = ioutil.TempDir("", "e2e-test-data"); err != nil {
		t.Fatal(err)
	}

	defer os.RemoveAll(cfgDir)
	defer os.RemoveAll(dataDir)

	cfgPath := filepath.Join(cfgDir, "prom.yaml")
	if err := ioutil.WriteFile(cfgPath, []byte(e2eTestPromConfig), 0666); err != nil {
		t.Fatal(err)
	}

	walDir := path.Join(dataDir, "wal")

	if err = os.MkdirAll(walDir, 0755); err != nil {
		t.Fatal(err)
	}

	ts := newTestServer(t, nil)

	// start gRPC service
	ts.runMetricsService()

	// Run prometheus
	promCmd := exec.CommandContext(
		ctx,
		"prometheus",
		"--storage.tsdb.path",
		dataDir,
		"--config.file",
		cfgPath,
		"--web.listen-address=0.0.0.0:19093",
	)
	promCmd.Stderr = os.Stderr
	promCmd.Stdout = os.Stdout
	if err := promCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer promCmd.Wait()
	defer promCmd.Process.Kill()

	// Start sidecar
	sideCmd := exec.CommandContext(
		ctx,
		os.Args[0],
		append(e2eTestMainCommonFlags,
			"--startup.timeout=15s",
			"--prometheus.wal", walDir,
			"--destination.attribute=service.name=Service",
		)...,
	)
	sideCmd.Env = append(os.Environ(), "RUN_MAIN=1")
	sideCmd.Stderr = os.Stderr
	sideCmd.Stdout = os.Stdout
	if err := sideCmd.Start(); err != nil {
		log.Fatal(err)
	}
	// Start scrape target
	go func() {
		scrapes := 1
		mux := http.NewServeMux()
		mux.HandleFunc("/metrics",
			func(w http.ResponseWriter, r *http.Request) {
				defer r.Body.Close()
				_, _ = ioutil.ReadAll(r.Body)

				w.WriteHeader(http.StatusOK)
				w.Write([]byte(fmt.Sprintf(
					e2eTestScrapeResultFmt,
					scrapes,
					scrapes,
				)))
				scrapes++
			},
		)
		s := &http.Server{
			Addr:    ":19002",
			Handler: mux,
		}
		ts.stops <- func() {
			_ = s.Close()
		}
		_ = s.ListenAndServe()
	}()
	go func() {
		scrapes := 1
		mux := http.NewServeMux()
		mux.HandleFunc("/metrics",
			func(w http.ResponseWriter, r *http.Request) {
				defer r.Body.Close()
				_, _ = ioutil.ReadAll(r.Body)

				w.WriteHeader(http.StatusOK)
				w.Write([]byte(fmt.Sprintf(
					e2eTestScrapeResultFmtRelabeled,
					scrapes,
					scrapes,
				)))
				scrapes++
			},
		)
		s := &http.Server{
			Addr:    ":19003",
			Handler: mux,
		}
		ts.stops <- func() {
			_ = s.Close()
		}
		_ = s.ListenAndServe()
	}()

	// Gather results
	var results []*metrics.ResourceMetrics
	resCount := 0
	for res := range ts.metrics {
		results = append(results, res)
		vs := otlptest.VisitorState{}
		vs.Visit(ctx, func(
			resource *resource.Resource,
			metricName string,
			kind config.Kind,
			monotonic bool,
			point interface{},
		) error {
			switch metricName {
			case "some_counter", "some_gauge", "some_counter_relabel", "some_gauge_relabel":
				// OK
				resCount++
			default:
				// Skip generated metrics.
			}
			return nil
		}, res)

		// Gather 2x the number of timeseries we want to see
		// for each, since they arrive out of order.
		if resCount >= 2*e2eMetricsPerScrape*e2eTestScrapes {
			break
		}
	}

	// Stop the external process.
	sideCmd.Process.Kill()
	sideCmd.Wait()

	// Stop the in-process services
	ts.Stop()

	// Validate data.
	output := map[string][]float64{}
	for _, res := range results {
		results = append(results, res)
		vs := otlptest.VisitorState{}
		vs.Visit(ctx, func(
			resource *resource.Resource,
			metricName string,
			kind config.Kind,
			monotonic bool,
			point interface{},
		) error {
			switch metricName {
			case "some_counter", "some_gauge", "some_counter_relabel", "some_gauge_relabel":
				// OK
			default:
				return nil
			}

			val := point.(*metrics.DoubleDataPoint).Value
			rvals := map[string]string{}
			for _, attr := range resource.Attributes {
				if _, has := rvals[attr.Key]; has {
					t.Error("duplicate resource key:", attr.Key)
					continue
				}
				rvals[attr.Key] = attr.Value.Value.(*common.AnyValue_StringValue).StringValue
			}

			// At this moment, the labels in static_configs are NOT
			// passed to the Resource.
			assert.Equal(t, rvals[string(semconv.ServiceNameKey)], "Service")
			assert.Equal(t, rvals[string(semconv.ServiceInstanceIDKey)], "")

			output[metricName] = append(output[metricName], val)
			return nil
		}, res)
	}

	// Sort and truncate each result, then compare.
	for key, values := range output {
		sort.Float64s(values)
		output[key] = values[0:e2eTestScrapes]
	}

	expect := map[string][]float64{
		"some_counter":         {0, 1, 2, 3, 4},
		"some_gauge":           {1, 2, 3, 4, 5},
		"some_counter_relabel": {0, 1, 2, 3, 4},
		"some_gauge_relabel":   {1, 2, 3, 4, 5},
	}

	if diff, equal := messagediff.PrettyDiff(output, expect); !equal {
		t.Errorf("unexpected result:\n%v", diff)
	}
}

// TODO: Move everything from here down into a separate file of test
// support (along with runPrometheusService from main_test.go).

func (ts *testServer) runMetricsService() {
	certificate, err := tls.LoadX509KeyPair(
		"testdata/certs/sidecar.test.crt",
		"testdata/certs/sidecar.test.key",
	)

	certPool := x509.NewCertPool()
	bs, err := ioutil.ReadFile("testdata/certs/root_ca.crt")
	if err != nil {
		log.Fatalf("failed to read client ca cert: %s", err)
	}

	ok := certPool.AppendCertsFromPEM(bs)
	if !ok {
		log.Fatal("failed to append client certs")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:19001")
	if err != nil {
		log.Fatalf("failed to listen: %s", err)
	}

	tlsConfig := &tls.Config{
		ClientAuth:   tls.NoClientCert,
		Certificates: []tls.Certificate{certificate},
		ClientCAs:    certPool,
	}

	serverOption := grpc.Creds(credentials.NewTLS(tlsConfig))
	grpcServer := grpc.NewServer(serverOption)
	metricService.RegisterMetricsServiceServer(grpcServer, ts)

	ctx, cancel := context.WithCancel(context.Background())
	ts.stops <- cancel

	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			log.Fatalf("failed to serve: %s", err)
		}
	}()

	go func() {
		<-ctx.Done()
		grpcServer.Stop()
	}()
}

// runDiagnosticsService is like runMetricService, but uses the
// insecure port and supports both trace and metrics.
func (ms *testServer) runDiagnosticsService(ts *traceServer) {
	listener, err := net.Listen("tcp", "127.0.0.1:19000")
	if err != nil {
		log.Fatalf("failed to listen: %s", err)
	}

	grpcServer := grpc.NewServer()
	metricService.RegisterMetricsServiceServer(grpcServer, ms)
	if ts != nil {
		traceService.RegisterTraceServiceServer(grpcServer, ts)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ms.stops <- cancel

	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			log.Fatalf("failed to serve: %s", err)
		}
	}()

	go func() {
		<-ctx.Done()
		grpcServer.Stop()
	}()
}

func (s *testServer) Export(ctx context.Context, req *metricService.ExportMetricsServiceRequest) (*metricService.ExportMetricsServiceResponse, error) {
	var emptyValue = metricService.ExportMetricsServiceResponse{}
	md, ok := grpcMetadata.FromIncomingContext(ctx)

	// Test the custom header is present
	if !ok {
		s.t.Error("Missing gRPC Headers")
		return &emptyValue, fmt.Errorf("Missing gRPC headers")
	}
	if len(md[e2eTestHeaderName]) != 1 || md[e2eTestHeaderName][0] != e2eTestHeaderValue {
		s.t.Error("Wrong gRPC header value", md)
		return &emptyValue, fmt.Errorf("Wrong gRPC header value")
	}

	for _, rm := range req.ResourceMetrics {
		s.metrics <- rm
	}

	require.NoError(s.t, grpc.SetTrailer(ctx, s.trailers))

	return &emptyValue, nil
}

func (s *traceServer) Export(ctx context.Context, req *traceService.ExportTraceServiceRequest) (*traceService.ExportTraceServiceResponse, error) {
	var emptyValue = traceService.ExportTraceServiceResponse{}
	md, ok := grpcMetadata.FromIncomingContext(ctx)

	// Test the custom header is present
	if !ok {
		s.t.Error("Missing gRPC Headers")
		return &emptyValue, fmt.Errorf("Missing gRPC headers")
	}
	if len(md[e2eTestHeaderName]) != 1 || md[e2eTestHeaderName][0] != e2eTestHeaderValue {
		s.t.Error("Wrong gRPC header value", md)
		return &emptyValue, fmt.Errorf("Wrong gRPC header value")
	}

	for _, ts := range req.ResourceSpans {
		s.spans <- ts
	}

	return &emptyValue, nil
}

func (s *testServer) Stop() {
	close(s.stops)

	for stop := range s.stops {
		stop()
	}
}

func newTestServer(t *testing.T, trailers grpcmeta.MD) *testServer {
	return &testServer{
		t:        t,
		stops:    make(chan func(), 3), // 3 = max number of stop functions registered
		metrics:  make(chan *metrics.ResourceMetrics, largeQueue),
		trailers: trailers,
	}
}

func (s *traceServer) Stop() {
	close(s.stops)

	for stop := range s.stops {
		stop()
	}
}

func newTraceServer(t *testing.T) *traceServer {
	return &traceServer{
		t:     t,
		stops: make(chan func(), 3), // 3 = max number of stop functions registered
		spans: make(chan *traces.ResourceSpans, largeQueue),
	}
}
