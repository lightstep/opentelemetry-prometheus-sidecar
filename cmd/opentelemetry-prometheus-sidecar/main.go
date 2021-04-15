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
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/google/uuid"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/cmd/internal"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/common"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/health"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/metadata"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/otlp"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/prometheus"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/retrieval"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/supervisor"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/pkg/errors"
	"github.com/prometheus/common/version"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/promql/parser"
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

// externalLabelPrefix is a non-standard convention for indicating
// external labels in the Prometheus data model, which are not
// semantically defined in OTel, as recognized by Lightstep.
const externalLabelPrefix = "__external_"

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
	logger := internal.NewLogger(cfg, isSupervisor)

	scfg := internal.SidecarConfig{
		ClientFactory:   nil,
		Monitor:         nil,
		Logger:          logger,
		InstanceId:      uuid.New().String(),
		Matchers:        [][]*labels.Matcher{},
		MetricRenames:   metricRenames,
		MetadataCache:   nil,
		MainConfig:      cfg,
		FailingReporter: common.NewFailingSet(log.With(logger, "component", "failing_metrics")),
	}

	telemetry.StaticSetup(scfg.Logger)

	telem := internal.StartTelemetry(
		scfg,
		"opentelemetry-prometheus-sidecar",
		isSupervisor,
	)
	if telem != nil {
		defer telem.Shutdown(context.Background())
	}

	// Start the supervisor.
	if isSupervisor {
		return startSupervisor(scfg, telem)
	}

	// Start the sidecar.  This context lasts the lifetime of the sidecar.
	ctx, cancelMain := telemetry.ContextWithSIGTERM(scfg.Logger)
	defer cancelMain()

	healthChecker := health.NewChecker(
		telem.Controller, scfg.Admin.HealthCheckPeriod.Duration, scfg.Logger, scfg.Admin.HealthCheckThresholdRatio,
	)

	httpClient := &http.Client{
		// Note: The Sidecar->Prometheus HTTP connection is not traced.
		// Transport: otelhttp.NewTransport(http.DefaultTransport),
	}

	scfg.Matchers, err = parseFilters(scfg.Filters)
	if err != nil {
		level.Error(scfg.Logger).Log("msg", "error parsing --filter", "err", err)
		return false
	}

	// Parse was validated already, ignore error.
	promURL, _ := url.Parse(scfg.Prometheus.Endpoint)

	scfg.Monitor = prometheus.NewMonitor(config.PromReady{
		Logger:                         log.With(scfg.Logger, "component", "prom_ready"),
		PromURL:                        promURL,
		StartupDelayEffectiveStartTime: time.Now(),
	})

	metadataURL, err := promURL.Parse(config.PrometheusMetadataEndpointPath)
	if err != nil {
		panic(err)
	}
	scfg.MetadataCache = metadata.NewCache(httpClient, metadataURL, staticMetadata)

	// Check the progress file, ensure we can write this file.
	startOffset, err := readWriteStartOffset(scfg)
	if err != nil {
		level.Error(scfg.Logger).Log("msg", "cannot write progress file", "err", err)
		return false
	}

	tailer, err := internal.NewTailer(ctx, scfg)
	if err != nil {
		level.Error(scfg.Logger).Log("msg", "tailing WAL failed", "err", err)
		return false
	}

	outputURL, _ := url.Parse(scfg.Destination.Endpoint)

	scfg.Destination.Headers[config.AgentKey] = config.AgentMainValue

	scfg.ClientFactory = internal.NewOTLPClientFactory(otlp.ClientConfig{
		Logger:           log.With(scfg.Logger, "component", "storage"),
		URL:              outputURL,
		Timeout:          scfg.Destination.Timeout.Duration,
		RootCertificates: scfg.Security.RootCertificates,
		Headers:          grpcMetadata.New(scfg.Destination.Headers),
		Compressor:       scfg.Destination.Compression,
		Prometheus:       scfg.Prometheus,
		FailingReporter:  scfg.FailingReporter,
	})

	// Start the admin server.
	go func() {
		defer cancelMain()

		server := newAdminServer(healthChecker, scfg.Admin, scfg.Logger)

		go func() {
			level.Debug(scfg.Logger).Log("msg", "starting admin server")
			<-ctx.Done()
			if err := server.Shutdown(context.Background()); err != nil {
				level.Error(scfg.Logger).Log("msg", "admin server shutdown", "err", err)
			}
		}()

		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			level.Error(scfg.Logger).Log("msg", "admin listener", "err", err)
		}
	}()

	logStartup(cfg, scfg.Logger)

	// Test for Prometheus and Outbound dependencies before starting.
	if err := selfTest(ctx, scfg); err != nil {
		level.Error(scfg.Logger).Log("msg", "selftest failed, not starting", "err", err)
		return false
	}

	level.Debug(scfg.Logger).Log("msg", "entering run state")
	healthChecker.SetRunning()

	if err = internal.StartComponents(ctx, scfg, tailer, startOffset); err != nil {
		cancelMain()
	}

	// SIGTERM causes graceful shutdown.
	level.Info(scfg.Logger).Log("msg", "sidecar process exiting")
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
func parseFilters(filters []string) ([][]*labels.Matcher, error) {
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

func selfTest(ctx context.Context, scfg internal.SidecarConfig) error {
	client := scfg.ClientFactory.New()

	ctx, cancel := context.WithTimeout(ctx, scfg.StartupTimeout.Duration)
	defer cancel()

	level.Debug(scfg.Logger).Log("msg", "checking Prometheus readiness")

	// These tests are performed sequentially, to keep the logs simple.
	// Note WaitForReady loops until success or stop if the context is canceled
	// or an unsupported version of prometheus is identified
	if err := scfg.Monitor.WaitForReady(ctx, cancel); err != nil {
		return errors.Wrap(err, "Prometheus is not ready")
	}

	level.Debug(scfg.Logger).Log("msg", "checking OpenTelemetry endpoint")

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

	level.Debug(scfg.Logger).Log("msg", "selftest was successful")
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
		level.Info(logger).Log("config", string(data))
	}

	if !cfg.DisableSupervisor {
		level.Debug(logger).Log("msg", "running under supervisor")
	}
}

func startSupervisor(scfg internal.SidecarConfig, telem *telemetry.Telemetry) bool {
	super := supervisor.New(supervisor.Config{
		Logger:    scfg.Logger,
		Admin:     scfg.Admin,
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
func readWriteStartOffset(scfg internal.SidecarConfig) (int, error) {
	startOffset, err := retrieval.ReadProgressFile(scfg.Prometheus.WAL)
	if err != nil {
		level.Warn(scfg.Logger).Log("msg", "reading progress file failed", "err", err)
		startOffset = 0
	}

	err = retrieval.SaveProgressFile(scfg.Prometheus.WAL, startOffset)
	return startOffset, err
}
