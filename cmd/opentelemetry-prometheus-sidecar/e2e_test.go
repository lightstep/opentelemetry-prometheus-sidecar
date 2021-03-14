package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	metricService "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/collector/metrics/v1"
	traceService "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/collector/trace/v1"
	common "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/common/v1"
	metrics "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/metrics/v1"
	traces "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/trace/v1"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/semconv"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	grpcMetadata "google.golang.org/grpc/metadata"
	grpcmeta "google.golang.org/grpc/metadata"
	messagediff "gopkg.in/d4l3k/messagediff.v1"
)

// Ports used here:
// 19001: OTLP service
// 19002: Scrape target
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
	e2eMetricsPerScrape = 2
	e2eTestScrapes      = 5

	e2eTestScrapeResultFmt = `
# HELP some_gauge Number of scrapes
# TYPE some_gauge gauge
some_gauge{kind="gauge"} %d

# HELP some_counter Number of scrapes
# TYPE some_counter counter
some_counter{kind="counter"} %d
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
`
)

func TestE2E(t *testing.T) {
	// Pipe for readiness check
	pipeRead, pipeWrite := io.Pipe()
	ready := make(chan struct{})
	defer pipeWrite.Close()

	go func() {
		// TODO: Replace this with /-/ready check using code elsewhere in this repo.
		defer pipeRead.Close()
		for {
			// This passes output to Stderr but signals
			// when the Prometheus server is ready.
			scanner := bufio.NewScanner(pipeRead)
			for scanner.Scan() {
				text := scanner.Text()
				if strings.Contains(text, "Server is ready to receive web requests.") {
					ready <- struct{}{}
				}
				_, _ = os.Stderr.WriteString(fmt.Sprintln(text))
			}
		}
	}()

	// Create config file
	var err error
	var cfgDir, dataDir string

	if cfgDir, err = ioutil.TempDir("", "e2e-test-cfg"); err != nil {
		log.Fatal(err)
	}
	if dataDir, err = ioutil.TempDir("", "e2e-test-data"); err != nil {
		log.Fatal(err)
	}

	defer os.RemoveAll(cfgDir)
	defer os.RemoveAll(dataDir)

	cfgPath := filepath.Join(cfgDir, "prom.yaml")
	if err := ioutil.WriteFile(cfgPath, []byte(e2eTestPromConfig), 0666); err != nil {
		log.Fatal(err)
	}

	// Cancel-able context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
	promCmd.Stderr = pipeWrite
	promCmd.Stdout = os.Stdout
	if err := promCmd.Start(); err != nil {
		log.Fatal(err)
	}
	defer promCmd.Wait()
	defer promCmd.Process.Kill()

	// Wait for Prometheus to be ready:
	select {
	case <-ready:
		// Good!
	case <-time.After(10 * time.Second):
		// Bad!
		log.Fatal("Prometheus did not start")
	}

	// Start sidecar
	sideCmd := exec.CommandContext(
		ctx,
		os.Args[0],
		append(e2eTestMainCommonFlags,
			"--prometheus.wal", path.Join(dataDir, "wal"),
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

	// Gather results
	var results []*metrics.ResourceMetrics
	for res := range ts.metrics {
		switch res.InstrumentationLibraryMetrics[0].Metrics[0].Name {
		case "some_counter", "some_gauge":
			// OK
		default:
			// Skip generated metrics.
			continue
		}
		results = append(results, res)

		// Gather 2x the number of timeseries we want to see
		// for each, since they arrive out of order.
		if len(results) == 2*e2eMetricsPerScrape*e2eTestScrapes {
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
	for _, result := range results {
		name := result.InstrumentationLibraryMetrics[0].Metrics[0].Name

		val := 0.0
		switch dp := result.InstrumentationLibraryMetrics[0].Metrics[0].Data.(type) {
		case *metrics.Metric_DoubleGauge:
			val = dp.DoubleGauge.DataPoints[0].Value
		case *metrics.Metric_DoubleSum:
			val = dp.DoubleSum.DataPoints[0].Value
		default:
			t.Error("Unexpected", result.InstrumentationLibraryMetrics[0].Metrics[0])
		}

		rvals := map[string]string{}
		for _, attr := range result.Resource.Attributes {
			if _, has := rvals[attr.Key]; has {
				t.Error("duplicate resource key:", attr.Key)
				continue
			}
			rvals[attr.Key] = attr.Value.Value.(*common.AnyValue_StringValue).StringValue
		}

		// At this moment, the labels in static_configs are NOT
		// passed to the Resource.
		assert.Equal(t, rvals[string(semconv.ServiceNameKey)], "Service")
		assert.NotEqual(t, rvals[string(semconv.ServiceInstanceIDKey)], "")

		output[name] = append(output[name], val)
	}

	// Sort and truncate each result, then compare.
	for key, values := range output {
		sort.Float64s(values)
		output[key] = values[0:e2eTestScrapes]
	}

	expect := map[string][]float64{
		"some_counter": []float64{1, 2, 3, 4, 5},
		"some_gauge":   []float64{1, 2, 3, 4, 5},
	}

	if diff, equal := messagediff.PrettyDiff(output, expect); !equal {
		t.Errorf("unexpected result:\n%v", diff)
	}
}

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
		telemetry.DefaultLogger().Log("msg", "starting test trace server")
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
		telemetry.DefaultLogger().Log("msg", "starting test metrics server")
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
	telemetry.DefaultLogger().Log("msg", "test metrics export")

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
	telemetry.DefaultLogger().Log("msg", "test trace export")

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
	telemetry.DefaultLogger().Log("msg", "stopping test metrics server")
	close(s.stops)

	for stop := range s.stops {
		stop()
	}
}

func newTestServer(t *testing.T, trailers grpcmeta.MD) *testServer {
	return &testServer{
		t:        t,
		stops:    make(chan func(), 3), // 3 = max number of stop functions registered
		metrics:  make(chan *metrics.ResourceMetrics, e2eTestScrapes),
		trailers: trailers,
	}
}

func (s *traceServer) Stop() {
	telemetry.DefaultLogger().Log("msg", "stopping test trace server")

	close(s.stops)

	for stop := range s.stops {
		stop()
	}
}

func newTraceServer(t *testing.T) *traceServer {
	return &traceServer{
		t:     t,
		stops: make(chan func(), 3), // 3 = max number of stop functions registered
		spans: make(chan *traces.ResourceSpans, e2eTestScrapes),
	}
}
