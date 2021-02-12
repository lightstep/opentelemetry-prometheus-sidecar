// Copyright 2015 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// The main package for the Prometheus server executable.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	_ "net/http/pprof" // Comment this line to disable pprof endpoint.
	"net/url"
	"os"
	"path"
	"runtime"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/cmd/internal"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/health"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/metadata"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/otlp"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/retrieval"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/supervisor"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/tail"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/oklog/run"
	"github.com/pkg/errors"
	"github.com/prometheus/common/version"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/promql/parser"
	grpcMetadata "google.golang.org/grpc/metadata"
)

// Note on metrics instrumentation relative ot the original OpenCensus
// instrumentation of this code base:
//
// - telemetry/* starts runtime and host instrumentation packages (includes uptime)
// - the net/http instrumentation package includes (spans and) metrics
// - the gRPC instrumentation package does not include metrics (but will eventually)
//
// TODO(jmacd): Await or add gRPC metrics instrumentation  in the upstream package.

// TODO(jmacd): Note that https://github.com/mwitkow/go-conntrack was removed, may
// be useful after other matters are resolved.

const supervisorEnv = "MAIN_SUPERVISOR"

func main() {
	if !Main() {
		os.Exit(1)
	}
}

func Main() bool {
	// Setup debugging helpers
	if os.Getenv("DEBUG") != "" {
		runtime.SetBlockProfileRate(20)
		runtime.SetMutexProfileFraction(20)
	}

	// Configure a from flags and/or a config file.
	cfg, metricRenames, staticMetadata, err := config.Configure(os.Args, ioutil.ReadFile)
	if err != nil {
		usage(err)
		return false
	}

	// Should this process act as supervisor?  This uses the supervisor
	// environment variable to avoid recursion.
	isSupervisor := !cfg.DisableSupervisor && os.Getenv(supervisorEnv) == ""

	// Configure logging and diagnostics.
	logger := internal.NewLogger(cfg, isSupervisor)

	telemetry.StaticSetup(logger)

	telem := internal.StartTelemetry(
		cfg,
		"opentelemetry-prometheus-sidecar",
		isSupervisor,
		logger,
	)
	if telem != nil {
		defer telem.Shutdown(context.Background())
	}

	// Start the supervisor.
	if isSupervisor {
		return startSupervisor(cfg, logger)
	}

	// Start the sidecar.  This context lasts the lifetime of the sidecar.
	ctx, cancelMain := telemetry.ContextWithSIGTERM(logger)
	defer cancelMain()

	healthChecker := health.NewChecker(telem.Controller)

	httpClient := &http.Client{
		// Note: The Sidecar->Prometheus HTTP connection is not traced.
		// Transport: otelhttp.NewTransport(http.DefaultTransport),
	}

	filters, err := parseFilters(logger, cfg.Filters)
	if err != nil {
		level.Error(logger).Log("msg", "error parsing --filter", "err", err)
		return false
	}

	// Parse was validated already, ignore error.
	promURL, _ := url.Parse(cfg.Prometheus.Endpoint)

	metadataURL, err := promURL.Parse(metadata.DefaultEndpointPath)
	if err != nil {
		panic(err)
	}
	metadataCache := metadata.NewCache(httpClient, metadataURL, staticMetadata)

	tailer, err := tail.Tail(
		ctx,
		log.With(logger, "component", "wal_reader"),
		cfg.Prometheus.WAL,
	)
	if err != nil {
		level.Error(logger).Log("msg", "tailing WAL failed", "err", err)
		return false
	}

	outputURL, _ := url.Parse(cfg.Destination.Endpoint)

	cfg.Destination.Headers[config.AgentKey] = config.AgentMainValue

	scf := internal.NewOTLPClientFactory(otlp.ClientConfig{
		Logger:           log.With(logger, "component", "storage"),
		URL:              outputURL,
		Timeout:          cfg.Destination.Timeout.Duration,
		RootCertificates: cfg.Security.RootCertificates,
		Headers:          grpcMetadata.New(cfg.Destination.Headers),
		Compressor:       cfg.Destination.Compression,
	})

	queueManager, err := otlp.NewQueueManager(
		log.With(logger, "component", "queue_manager"),
		config.DefaultQueueConfig(),
		cfg.Destination.Timeout.Duration,
		scf,
		tailer,
	)
	if err != nil {
		level.Error(logger).Log("msg", "creating queue manager failed", "err", err)
		return false
	}

	prometheusReader := retrieval.NewPrometheusReader(
		log.With(logger, "component", "prom_wal"),
		cfg.Prometheus.WAL,
		tailer,
		filters,
		metricRenames,
		metadataCache,
		queueManager,
		cfg.OpenTelemetry.MetricsPrefix,
		cfg.Prometheus.MaxPointAge.Duration,
		labels.FromMap(cfg.Destination.Attributes),
	)

	// Start the admin server.
	go func() {
		defer cancelMain()

		server := newAdminServer(healthChecker, cfg.Admin, logger)

		go func() {
			level.Debug(logger).Log("msg", "starting admin server")
			<-ctx.Done()
			if err := server.Shutdown(context.Background()); err != nil {
				level.Error(logger).Log("msg", "admin server shutdown", "err", err)
			}
		}()

		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			level.Error(logger).Log("msg", "admin listener", "err", err)
		}
	}()

	// Check the progress file, ensure we can write this file.
	startOffset, err := readWriteStartOffset(cfg, logger)
	if err != nil {
		level.Error(logger).Log("msg", "cannot write progress file", "err", err)
		return false
	}

	logStartup(cfg, logger)

	// Test for Prometheus and Outbound dependencies before starting.
	if err := selfTest(ctx, promURL, scf, cfg.StartupTimeout.Duration, logger); err != nil {
		level.Error(logger).Log("msg", "selftest failed, not starting", "err", err)
		return false
	}

	// Sleep to allow the first scrapes to complete.
	level.Debug(logger).Log("msg", "sleeping to allow Prometheus its first scrape")
	select {
	case <-time.After(cfg.StartupDelay.Duration):
	case <-ctx.Done():
		return true
	}

	level.Debug(logger).Log("msg", "starting now")
	healthChecker.SetReady(true)

	// Run two inter-depdendent components:
	// (1) Prometheus reader
	// (2) Queue manager
	// TODO: Replace this with x/sync/errgroup
	var g run.Group
	{
		g.Add(
			func() error {
				err = prometheusReader.Run(ctx, startOffset)
				level.Info(logger).Log("msg", "Prometheus reader stopped")
				return err
			},
			func(err error) {
				// Prometheus reader needs to be stopped before closing the TSDB
				// so that it doesn't try to write samples to a closed storage.
				level.Info(logger).Log("msg", "Stopping Prometheus reader...")
				cancelMain()
			},
		)
	}
	{
		stopCh := make(chan struct{})
		g.Add(
			func() error {
				if err := queueManager.Start(); err != nil {
					return err
				}
				level.Info(logger).Log("msg", "OpenTelemetry client started")
				<-stopCh
				return nil
			},
			func(err error) {
				if err := queueManager.Stop(); err != nil {
					level.Error(logger).Log("msg", "Error stopping OpenTelemetry writer", "err", err)
				}
				close(stopCh)
			},
		)
	}
	if err := g.Run(); err != nil {
		level.Error(logger).Log("err", err)
		return false
	}

	// SIGTERM causes graceful shutdown.
	level.Info(logger).Log("msg", "sidecar process exiting")
	return true
}

func usage(err error) {
	fmt.Fprintf(
		os.Stderr,
		"run '%s --help' for usage and configuration syntax: %v\n",
		os.Args[0],
		err,
	)
}

func waitForPrometheus(ctx context.Context, logger log.Logger, promURL *url.URL) bool {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()

	u := *promURL
	u.Path = path.Join(promURL.Path, "/-/ready")

	for {
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
			resp, err := http.Get(u.String())
			if err != nil {
				level.Warn(logger).Log("msg", "Prometheus readiness check", "err", err)
				continue
			}
			if resp.StatusCode/100 == 2 {
				return true
			}

			level.Warn(logger).Log("msg", "Prometheus is not ready", "status", resp.Status)
		}
	}
}

// parseFilters parses two flags that contain PromQL-style metric/label selectors and
// returns a list of the resulting matchers.
func parseFilters(logger log.Logger, filters []string) ([][]*labels.Matcher, error) {
	var matchers [][]*labels.Matcher
	for _, f := range filters {
		m, err := parser.ParseMetricSelector(f)
		if err != nil {
			return nil, errors.Errorf("cannot parse filter '%s': %q", f, err)
		}
		matchers = append(matchers, m)
	}
	return matchers, nil
}

func selfTest(ctx context.Context, promURL *url.URL, scf otlp.StorageClientFactory, timeout time.Duration, logger log.Logger) error {
	client := scf.New()

	level.Debug(logger).Log("msg", "starting selftest")

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// These tests are performed sequentially, to keep the logs simple.
	// Note waitForPrometheus has no unrecoverable error conditions, so
	// loops until success or the context is canceled.
	if !waitForPrometheus(ctx, logger, promURL) {
		return fmt.Errorf("Prometheus is not ready")
	}

	// Outbound connection test.
	{
		if err := client.Selftest(ctx); err != nil {
			_ = client.Close()
			return fmt.Errorf("could not send test opentelemetry.ExportMetricsServiceRequest request: %w", err)
		}

		if err := client.Close(); err != nil {
			return fmt.Errorf("error closing test client: %w", err)
		}
	}

	level.Debug(logger).Log("msg", "selftest was successful")
	return nil
}

func logStartup(cfg config.MainConfig, logger log.Logger) {
	level.Info(logger).Log(
		"msg", "Starting OpenTelemetry Prometheus sidecar",
		"version", version.Info(),
		"build_context", version.BuildContext(),
		"host_details", Uname(),
		"fd_limits", FdLimits(),
	)

	if data, err := json.Marshal(cfg); err == nil {
		level.Debug(logger).Log("config", string(data))
	}

	if !cfg.DisableSupervisor {
		level.Debug(logger).Log("msg", "running under supervisor")
	}
}

func startSupervisor(cfg config.MainConfig, logger log.Logger) bool {
	super := supervisor.New(supervisor.Config{
		Logger: logger,
		Admin:  cfg.Admin,

		// Note: the metrics reporting interval is not
		// configurable (see start_telemetry.go), but whatever
		// it is we should poll at with a longer period to be
		// sure a collection happens between health checks.
		Period: time.Duration(float64(config.DefaultReportingPeriod) * 2),
	})

	os.Setenv(supervisorEnv, "active")

	return super.Run(os.Args)
}

func newAdminServer(hc *health.Checker, acfg config.AdminConfig, logger log.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/-/health", hc.Health())
	mux.Handle("/-/ready", hc.Ready())
	address := fmt.Sprint(acfg.ListenIP, ":", acfg.Port)
	return &http.Server{
		Addr:    address,
		Handler: mux,
	}
}

// readWriteStartOffset reads the last (approxiate) progress position and re-writes
// the progress file, to ensure we have write permission on startup.
func readWriteStartOffset(cfg config.MainConfig, logger log.Logger) (int, error) {
	startOffset, err := retrieval.ReadProgressFile(cfg.Prometheus.WAL)
	if err != nil {
		level.Warn(logger).Log("msg", "reading progress file failed", "err", err)
		startOffset = 0
	}

	err = retrieval.SaveProgressFile(cfg.Prometheus.WAL, startOffset)
	return startOffset, err
}
