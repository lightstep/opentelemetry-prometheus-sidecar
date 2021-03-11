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
	"runtime"
	"strings"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/google/uuid"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/cmd/internal"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/health"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/metadata"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/otlp"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/prometheus"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/retrieval"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/supervisor"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/tail"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/oklog/run"
	"github.com/pkg/errors"
	"github.com/prometheus/common/version"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/prometheus/tsdb/wal"
	"go.opentelemetry.io/otel/semconv"
	grpcMetadata "google.golang.org/grpc/metadata"

	// register grpc compressors
	_ "github.com/lightstep/opentelemetry-prometheus-sidecar/snappy"
	_ "google.golang.org/grpc/encoding/gzip"
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

func runReader(ctx context.Context, reader *retrieval.PrometheusReader, walDir string, startOffset int) error {
	var err error
	for {
		err = reader.Run(ctx, startOffset)
		// TODO: add a retry configuration value to ensure we dont loop indefinitely
		if err != nil && strings.Contains(err.Error(), tail.ErrSkipSegment.Error()) {
			_ = retrieval.SaveProgressFile(walDir, startOffset)
			reader.Next()
			startOffset = reader.CurrentSegment() * wal.DefaultSegmentSize
			continue
		}
		return err
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

	// Unique identifer for this process.
	svcInstanceId := uuid.New().String()

	// Configure logging and diagnostics.
	logger := internal.NewLogger(cfg, isSupervisor)

	telemetry.StaticSetup(logger)

	telem := internal.StartTelemetry(
		cfg,
		"opentelemetry-prometheus-sidecar",
		svcInstanceId,
		isSupervisor,
		logger,
	)
	if telem != nil {
		defer telem.Shutdown(context.Background())
	}

	// Start the supervisor.
	if isSupervisor {
		return startSupervisor(cfg, telem, logger)
	}

	// Start the sidecar.  This context lasts the lifetime of the sidecar.
	ctx, cancelMain := telemetry.ContextWithSIGTERM(logger)
	defer cancelMain()

	healthChecker := health.NewChecker(
		telem.Controller, cfg.Admin.HealthCheckPeriod.Duration, logger, cfg.Admin.HealthCheckThresholdRatio,
	)

	httpClient := &http.Client{
		// Note: The Sidecar->Prometheus HTTP connection is not traced.
		// Transport: otelhttp.NewTransport(http.DefaultTransport),
	}

	filters, err := parseFilters(logger, cfg.Filters)
	if err != nil {
		level.Error(logger).Log("msg", "error parsing --filter", "err", err)
		return false
	}

	intervals, err := parseIntervals(cfg.Prometheus.ScrapeIntervals)
	if err != nil {
		level.Error(logger).Log("msg", "error parsing --prometheus.scrape-interval", "err", err)
		return false
	}

	// Parse was validated already, ignore error.
	promURL, _ := url.Parse(cfg.Prometheus.Endpoint)

	readyCfg := config.PromReady{
		Logger:          log.With(logger, "component", "prom_ready"),
		PromURL:         promURL,
		ScrapeIntervals: intervals,
	}

	metadataURL, err := promURL.Parse(metadata.DefaultEndpointPath)
	if err != nil {
		panic(err)
	}
	metadataCache := metadata.NewCache(httpClient, metadataURL, staticMetadata)

	// Check the progress file, ensure we can write this file.
	startOffset, err := readWriteStartOffset(cfg, logger)
	if err != nil {
		level.Error(logger).Log("msg", "cannot write progress file", "err", err)
		return false
	}

	tailer, err := tail.Tail(
		ctx,
		log.With(logger, "component", "wal_reader"),
		cfg.Prometheus.WAL,
		readyCfg,
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
		Prometheus:       cfg.Prometheus,
	})

	queueManager, err := otlp.NewQueueManager(
		log.With(logger, "component", "queue_manager"),
		cfg.QueueConfig(),
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
		createResourceLabels(svcInstanceId, cfg.Destination.Attributes),
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

	logStartup(cfg, logger)

	// Test for Prometheus and Outbound dependencies before starting.
	if err := selfTest(ctx, scf, cfg.StartupTimeout.Duration, logger, readyCfg); err != nil {
		level.Error(logger).Log("msg", "selftest failed, not starting", "err", err)
		return false
	}

	level.Debug(logger).Log("msg", "entering run state")
	healthChecker.SetRunning()

	// Run two inter-depdendent components:
	// (1) Prometheus reader
	// (2) Queue manager
	// TODO: Replace this with x/sync/errgroup
	var g run.Group
	{
		g.Add(
			func() error {
				level.Info(logger).Log("msg", "starting Prometheus reader", "segment", startOffset/wal.DefaultSegmentSize)
				return runReader(ctx, prometheusReader, cfg.Prometheus.WAL, startOffset)
			},
			func(err error) {
				// Prometheus reader needs to be stopped before closing the TSDB
				// so that it doesn't try to write samples to a closed storage.
				// See the use of `stopCh` below to explain how this works.
				level.Info(logger).Log("msg", "stopping Prometheus reader")
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
				level.Info(logger).Log("msg", "starting OpenTelemetry writer")
				<-stopCh
				return nil
			},
			func(err error) {
				if err := queueManager.Stop(); err != nil {
					level.Error(logger).Log(
						"msg", "stopping OpenTelemetry writer",
						"err", err,
					)
				}
				close(stopCh)
			},
		)
	}
	if err := g.Run(); err != nil {
		level.Error(logger).Log("msg", "run loop error", "err", err)
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

func createResourceLabels(svcInstanceId string, extraLabels map[string]string) labels.Labels {
	extraLabels[string(semconv.ServiceInstanceIDKey)] = svcInstanceId
	return labels.FromMap(extraLabels)
}

func selfTest(ctx context.Context, scf otlp.StorageClientFactory, timeout time.Duration, logger log.Logger, readyCfg config.PromReady) error {
	client := scf.New()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	level.Debug(logger).Log("msg", "checking Prometheus readiness")

	// These tests are performed sequentially, to keep the logs simple.
	// Note WaitForReady loops until success or stop if the context is canceled
	// or an unsupported version of prometheus is identified
	if err := prometheus.WaitForReady(ctx, cancel, readyCfg); err != nil {
		return errors.Wrap(err, "Prometheus is not ready")
	}

	level.Debug(logger).Log("msg", "checking OpenTelemetry endpoint")

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
		"msg", "starting OpenTelemetry Prometheus sidecar",
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

func startSupervisor(cfg config.MainConfig, telem *telemetry.Telemetry, logger log.Logger) bool {
	super := supervisor.New(supervisor.Config{
		Logger:    logger,
		Admin:     cfg.Admin,
		Telemetry: telem,
	})

	os.Setenv(supervisorEnv, "active")

	return super.Run(os.Args)
}

func newAdminServer(hc *health.Checker, acfg config.AdminConfig, logger log.Logger) *http.Server {
	mux := http.NewServeMux()

	mux.Handle(config.HealthCheckURI, hc.Alive())

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

func parseIntervals(ss []string) (dd []time.Duration, _ error) {
	for _, s := range ss {
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, errors.Wrap(err, "parse duration "+s)
		}
		dd = append(dd, d)
	}
	return
}
