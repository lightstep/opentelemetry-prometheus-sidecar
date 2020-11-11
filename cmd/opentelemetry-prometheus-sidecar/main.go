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
	"net"
	"net/http"
	_ "net/http/pprof" // Comment this line to disable pprof endpoint.
	"net/url"
	"os"
	"os/signal"
	"path"
	"runtime"
	"syscall"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/metadata"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/otlp"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/retrieval"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/tail"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/targets"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	conntrack "github.com/mwitkow/go-conntrack"
	"github.com/oklog/oklog/pkg/group"
	"github.com/pkg/errors"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/version"
	promconfig "github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/promql/parser"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	grpcMetadata "google.golang.org/grpc/metadata"
)

// TODO(jmacd): Define a path for healthchecks.

// var (
// 	sizeDistribution    = view.Distribution(0, 1024, 2048, 4096, 16384, 65536, 262144, 1048576, 4194304, 33554432)
// 	latencyDistribution = view.Distribution(0, 1, 2, 5, 10, 15, 25, 50, 100, 200, 400, 800, 1500, 3000, 6000)

// 	// VersionTag identifies the version of this binary.
// 	VersionTag = tag.MustNewKey("version")
// 	// UptimeMeasure is a cumulative metric.
// 	UptimeMeasure = stats.Int64(
// 		"agent.googleapis.com/agent/uptime",
// 		"uptime of the OpenTelemetry Prometheus collector",
// 		stats.UnitSeconds)
// )

// func init() {
// 	prometheus.MustRegister(version.NewCollector("prometheus"))

// 	if err := view.Register(
// 		&view.View{
// 			Name:        "opencensus.io/http/client/request_count",
// 			Description: "Count of HTTP requests started",
// 			Measure:     ochttp.ClientRequestCount,
// 			TagKeys:     []tag.Key{ochttp.Method, ochttp.Path},
// 			Aggregation: view.Count(),
// 		},
// 		&view.View{
// 			Name:        "opencensus.io/http/client/request_bytes",
// 			Description: "Size distribution of HTTP request body",
// 			Measure:     ochttp.ClientRequestBytes,
// 			TagKeys:     []tag.Key{ochttp.Method, ochttp.StatusCode, ochttp.Path},
// 			Aggregation: sizeDistribution,
// 		},
// 		&view.View{
// 			Name:        "opencensus.io/http/client/response_bytes",
// 			Description: "Size distribution of HTTP response body",
// 			Measure:     ochttp.ClientResponseBytes,
// 			TagKeys:     []tag.Key{ochttp.Method, ochttp.StatusCode, ochttp.Path},
// 			Aggregation: sizeDistribution,
// 		},
// 		&view.View{
// 			Name:        "opencensus.io/http/client/latency",
// 			Description: "Latency distribution of HTTP requests",
// 			TagKeys:     []tag.Key{ochttp.Method, ochttp.StatusCode, ochttp.Path},
// 			Measure:     ochttp.ClientLatency,
// 			Aggregation: latencyDistribution,
// 		},
// 	); err != nil {
// 		panic(err)
// 	}
// 	if err := view.Register(
// 		ocgrpc.DefaultClientViews...,
// 	); err != nil {
// 		panic(err)
// 	}
// 	if err := view.Register(
// 		&view.View{
// 			Measure:     UptimeMeasure,
// 			TagKeys:     []tag.Key{VersionTag},
// 			Aggregation: view.Sum(),
// 		},
// 	); err != nil {
// 		panic(err)
// 	}
// }

func main() {
	if os.Getenv("DEBUG") != "" {
		runtime.SetBlockProfileRate(20)
		runtime.SetMutexProfileFraction(20)
	}

	cfg, metricRenames, staticMetadata, err := config.Configure(os.Args, ioutil.ReadFile)
	if err != nil {
		usage(err)
		os.Exit(2)
	}

	var plc promlog.Config
	plc.Level = &promlog.AllowedLevel{}
	plc.Format = &promlog.AllowedFormat{}
	plc.Level.Set(cfg.LogConfig.Level)
	plc.Format.Set(cfg.LogConfig.Format)
	logger := promlog.New(&plc)

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

	// We avoided using the kingpin support for URL flags because
	// it leads to special cases merging configs and because URL
	// parsing succeeds in cases w/o a scheme, needs to be
	// validated anyway.
	type namedURL struct {
		name       string
		value      string
		allowEmpty bool
	}
	for _, pair := range []namedURL{
		{"destination.endpoint", cfg.Destination.Endpoint, false},
		{"diagnostics.endpoint", cfg.Diagnostics.Endpoint, true},
		{"prometheus.endpoint", cfg.Prometheus.Endpoint, false},
	} {
		if pair.allowEmpty && pair.value == "" {
			continue
		}
		if pair.value == "" {
			level.Error(logger).Log("msg", "endpoint must be set", "name", pair.name)
			os.Exit(2)
		}
		url, err := url.Parse(pair.value)
		if err != nil {
			level.Error(logger).Log("msg", "invalid endpoint", "name", pair.name, "endpoint", pair.value, "error", err)
			os.Exit(2)
		}

		switch url.Scheme {
		case "http", "https":
			// Good!
		default:
			level.Error(logger).Log("msg", "endpoints must use http or https", "name", pair.name, "endpoint", pair.value)
			os.Exit(2)
		}
	}

	if cfg.Diagnostics.Endpoint != "" {
		endpoint, _ := url.Parse(cfg.Diagnostics.Endpoint)
		hostport := endpoint.Hostname()
		if len(endpoint.Port()) > 0 {
			hostport = net.JoinHostPort(hostport, endpoint.Port())
		}
		// Set a service.name resource if none is set.
		const serviceNameKey = "service.name"
		if _, ok := cfg.Diagnostics.Attributes[serviceNameKey]; !ok {
			cfg.Diagnostics.Attributes[serviceNameKey] = "opentelemetry-prometheus-sidecar"
		}

		defer telemetry.ConfigureOpentelemetry(
			telemetry.WithExporterEndpoint(hostport),
			telemetry.WithExporterInsecure(endpoint.Scheme == "http"),
			telemetry.WithLogger(log.With(logger, "component", "telemetry")),
			telemetry.WithHeaders(cfg.Diagnostics.Headers),
			telemetry.WithResourceAttributes(cfg.Diagnostics.Attributes),
		).Shutdown()
	}

	// We instantiate a context here since the tailer is used by two other components.
	// The context will be used in the lifecycle of prometheusReader further down.
	ctx, cancel := context.WithCancel(context.Background())

	// go func() {
	// 	uptimeUpdateTime := time.Now()
	// 	c := time.Tick(60 * time.Second)
	// 	for now := range c {
	// 		stats.RecordWithTags(ctx,
	// 			[]tag.Mutator{tag.Upsert(VersionTag, fmt.Sprintf("opentelemetry-prometheus-sidecar/%s", version.Version))},
	// 			UptimeMeasure.M(int64(now.Sub(uptimeUpdateTime).Seconds())))
	// 		uptimeUpdateTime = now
	// 	}
	// }()

	httpClient := &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}

	filters, err := parseFilters(logger, cfg.Filters)
	if err != nil {
		level.Error(logger).Log("msg", "error parsing --filter", "err", err)
		os.Exit(2)
	}

	// Parse was validated already, ignore error.
	promURL, _ := url.Parse(cfg.Prometheus.Endpoint)

	targetsURL, err := promURL.Parse(targets.DefaultAPIEndpoint)
	if err != nil {
		level.Error(logger).Log("msg", "error parsing --prometheus.endpoint", "err", err)
		os.Exit(2)
	}

	targetCache := targets.NewCache(
		logger,
		httpClient,
		targetsURL,
		labels.FromMap(cfg.Destination.Attributes),
		cfg.OpenTelemetry.UseMetaLabels,
	)

	metadataURL, err := promURL.Parse(metadata.DefaultEndpointPath)
	if err != nil {
		panic(err)
	}
	metadataCache := metadata.NewCache(httpClient, metadataURL, staticMetadata)

	tailer, err := tail.Tail(ctx, cfg.Prometheus.WAL)
	if err != nil {
		level.Error(logger).Log("msg", "tailing WAL failed", "err", err)
		os.Exit(1)
	}
	promconfig.DefaultQueueConfig.MaxSamplesPerSend = otlp.MaxTimeseriesesPerRequest
	// We want the queues to have enough buffer to ensure consistent flow with full batches
	// being available for every new request.
	// Testing with different latencies and shard numbers have shown that 3x of the batch size
	// works well.
	promconfig.DefaultQueueConfig.Capacity = 3 * otlp.MaxTimeseriesesPerRequest

	outputURL, _ := url.Parse(cfg.Destination.Endpoint)

	var scf otlp.StorageClientFactory = &otlpClientFactory{
		logger:   log.With(logger, "component", "storage"),
		url:      outputURL,
		timeout:  10 * time.Second,
		security: cfg.Security,
		headers:  grpcMetadata.New(cfg.Destination.Headers),
	}

	queueManager, err := otlp.NewQueueManager(
		log.With(logger, "component", "queue_manager"),
		promconfig.DefaultQueueConfig,
		scf,
		tailer,
	)
	if err != nil {
		level.Error(logger).Log("msg", "creating queue manager failed", "err", err)
		os.Exit(1)
	}

	prometheusReader := retrieval.NewPrometheusReader(
		log.With(logger, "component", "prom_wal"),
		cfg.Prometheus.WAL,
		tailer,
		filters,
		metricRenames,
		targetCache,
		metadataCache,
		queueManager,
		cfg.OpenTelemetry.MetricsPrefix,
	)

	// Monitor outgoing connections on default transport with conntrack.
	http.DefaultTransport.(*http.Transport).DialContext = conntrack.NewDialContextFunc(
		conntrack.DialWithTracing(),
	)

	var g group.Group
	{
		ctx, cancel := context.WithCancel(context.Background())
		g.Add(func() error {
			targetCache.Run(ctx)
			return nil
		}, func(error) {
			cancel()
		})
	}
	{
		term := make(chan os.Signal)
		signal.Notify(term, os.Interrupt, syscall.SIGTERM)
		cancel := make(chan struct{})
		g.Add(
			func() error {
				// Don't forget to release the reloadReady channel so that waiting blocks can exit normally.
				select {
				case <-term:
					level.Warn(logger).Log("msg", "Received SIGTERM, exiting gracefully...")
				case <-cancel:
					break
				}
				return nil
			},
			func(err error) {
				close(cancel)
			},
		)
	}
	{
		// We use the context we defined higher up instead of a local one like in the other actors.
		// This is necessary since it's also used to manage the tailer's lifecycle, which the reader
		// depends on to exit properly.
		g.Add(
			func() error {
				startOffset, err := retrieval.ReadProgressFile(cfg.Prometheus.WAL)
				if err != nil {
					level.Warn(logger).Log("msg", "reading progress file failed", "err", err)
					startOffset = 0
				}
				// Write the file again once to ensure we have write permission on startup.
				if err := retrieval.SaveProgressFile(cfg.Prometheus.WAL, startOffset); err != nil {
					return err
				}
				waitForPrometheus(ctx, logger, promURL)
				// Sleep a fixed amount of time to allow the first scrapes to complete.
				select {
				case <-time.After(cfg.StartupDelay.Duration):
				case <-ctx.Done():
					return nil
				}
				err = prometheusReader.Run(ctx, startOffset)
				level.Info(logger).Log("msg", "Prometheus reader stopped")
				return err
			},
			func(err error) {
				// Prometheus reader needs to be stopped before closing the TSDB
				// so that it doesn't try to write samples to a closed storage.
				level.Info(logger).Log("msg", "Stopping Prometheus reader...")
				cancel()
			},
		)
	}
	{
		cancel := make(chan struct{})
		g.Add(
			func() error {
				if err := queueManager.Start(); err != nil {
					return err
				}
				level.Info(logger).Log("msg", "OpenTelemetry client started")
				<-cancel
				return nil
			},
			func(err error) {
				if err := queueManager.Stop(); err != nil {
					level.Error(logger).Log("msg", "Error stopping OpenTelemetry writer", "err", err)
				}
				close(cancel)
			},
		)
	}
	{
		cancel := make(chan struct{})
		server := &http.Server{
			Addr: cfg.Admin.ListenAddress,
		}
		g.Add(
			func() error {
				level.Info(logger).Log("msg", "Web server started")
				err := server.ListenAndServe()
				if err != http.ErrServerClosed {
					return err
				}
				<-cancel
				return nil
			},
			func(err error) {
				if err := server.Shutdown(context.Background()); err != nil {
					level.Error(logger).Log("msg", "Error stopping web server", "err", err)
				}
				close(cancel)
			},
		)
	}
	if err := g.Run(); err != nil {
		level.Error(logger).Log("err", err)
	}
	level.Info(logger).Log("msg", "See you next time!")
}

func usage(err error) {
	fmt.Fprintf(
		os.Stderr,
		"run '%s --help' for usage and configuration syntax: %v\n",
		os.Args[0],
		err,
	)
}

type otlpClientFactory struct {
	logger   log.Logger
	url      *url.URL
	timeout  time.Duration
	security config.SecurityConfig
	headers  grpcMetadata.MD
}

func (s *otlpClientFactory) New() otlp.StorageClient {
	return otlp.NewClient(&otlp.ClientConfig{
		Logger:           s.logger,
		URL:              s.url,
		Timeout:          s.timeout,
		RootCertificates: s.security.RootCertificates,
		Headers:          s.headers,
	})
}

func (s *otlpClientFactory) Name() string {
	return s.url.String()
}

func waitForPrometheus(ctx context.Context, logger log.Logger, promURL *url.URL) {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()

	u := *promURL
	u.Path = path.Join(promURL.Path, "/-/ready")

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			resp, err := http.Get(u.String())
			if err != nil {
				level.Warn(logger).Log("msg", "query Prometheus readiness", "err", err)
				continue
			}
			if resp.StatusCode/100 == 2 {
				return
			}
			level.Warn(logger).Log("msg", "Prometheus not ready", "status", resp.Status)
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
