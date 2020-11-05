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
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/ghodss/yaml"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/metadata"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/otlp"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/retrieval"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/tail"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/targets"
	conntrack "github.com/mwitkow/go-conntrack"
	"github.com/oklog/oklog/pkg/group"
	"github.com/pkg/errors"
	"github.com/prometheus/common/promlog"
	promlogflag "github.com/prometheus/common/promlog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/prometheus/promql/parser"
	grpcMetadata "google.golang.org/grpc/metadata"
	kingpin "gopkg.in/alecthomas/kingpin.v2"
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

const (
	DefaultStartupDelay       = time.Minute
	DefaultWALDirectory       = "data/wal"
	DefaultAdminListenAddress = "0.0.0.0:9091"
	DefaultPrometheusEndpoint = "http://127.0.0.1:9090/"

	briefDescription = `
The OpenTelemetry Prometheus sidecar runs alongside the
Prometheus (https://prometheus.io/) Server and sends metrics data to
an OpenTelemetry (https://opentelemetry.io) Protocol endpoint.
`
)

type MetricRenamesConfig struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type StaticMetadataConfig struct {
	Metric    string `json:"metric"`
	Type      string `json:"type"`
	ValueType string `json:"value_type"`
	Help      string `json:"help"`
}

type SecurityConfig struct {
	RootCertificates []string `json:"root_certificates"`
}

type DurationConfig struct {
	time.Duration `json:"duration" yaml:"-,inline"`
}

type OTLPConfig struct {
	Attributes map[string]string `json:"attributes"`
	Endpoint   string            `json:"endpoint"`
	Headers    map[string]string `json:"headers"`
}

type LogConfig struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

type PromConfig struct {
	WAL      string `json:"wal"`
	Endpoint string `json:"endpoint"`
}

type OTelConfig struct {
	MetricsPrefix string `json:"metrics_prefix"`
	UseMetaLabels bool   `json:"use_meta_labels"`
}

type AdminConfig struct {
	ListenAddress string `json:"listen_address"`
}

type MainConfig struct {
	// General
	ConfigFilename string         `json:"-"`
	Security       SecurityConfig `json:"security"`
	Admin          AdminConfig    `json:"admin"`
	StartupDelay   DurationConfig `json:"startup_delay"`

	// Prometheus input
	Prometheus PromConfig `json:"prometheus"`

	// Opentelemetry setup
	OpenTelemetry OTelConfig `json:"opentelemetry"`

	// Primary output
	Destination OTLPConfig `json:"destination"`

	// Diagnostic output
	LogConfig LogConfig `json:"log_config"`

	// Sidecar customization
	Filtersets     []string               `json:"filter_sets"`
	MetricRenames  []MetricRenamesConfig  `json:"metric_renames"`
	StaticMetadata []StaticMetadataConfig `json:"static_metadata"`
}

type fileReadFunc func(filename string) ([]byte, error)

func DefaultMainConfig() MainConfig {
	return MainConfig{
		Prometheus: PromConfig{
			WAL:      DefaultWALDirectory,
			Endpoint: DefaultPrometheusEndpoint,
		},
		Admin: AdminConfig{
			ListenAddress: DefaultAdminListenAddress,
		},
		Destination: OTLPConfig{
			Headers:    map[string]string{},
			Attributes: map[string]string{},
		},
		LogConfig: LogConfig{
			Level:  "info",
			Format: "logfmt",
		},
		StartupDelay: DurationConfig{
			DefaultStartupDelay,
		},
	}
}

// Configure is a separate unit of code for testing purposes.
func Configure(args []string, readFunc fileReadFunc) (MainConfig, map[string]string, []*metadata.Entry, error) {
	cfg := DefaultMainConfig()

	a := kingpin.New(filepath.Base(args[0]), briefDescription)

	a.Version(version.Print("opentelemetry-prometheus-sidecar"))

	a.HelpFlag.Short('h')

	// Below we avoid using the kingpin.v2 `Default()` mechanism
	// so that file config overrides default config and flag
	// config overrides file config.

	a.Flag("config-file", "A configuration file.").
		StringVar(&cfg.ConfigFilename)

	a.Flag("destination.endpoint", "Address of the OpenTelemetry Metrics protocol (gRPC) endpoint (e.g., https://host:port).  Use \"http\" (not \"https\") for an insecure connection.").
		StringVar(&cfg.Destination.Endpoint)

	a.Flag("destination.attribute", "Attributes for exported metrics (e.g., MyResource=Value1). May be repeated.").
		StringMapVar(&cfg.Destination.Attributes)

	a.Flag("destination.header", "Headers for gRPC connection (e.g., MyHeader=Value1). May be repeated.").
		StringMapVar(&cfg.Destination.Headers)

	a.Flag("prometheus.wal", "Directory from where to read the Prometheus TSDB WAL. Default: "+DefaultWALDirectory).
		StringVar(&cfg.Prometheus.WAL)

	a.Flag("prometheus.endpoint", "Endpoint where Prometheus hosts its  UI, API, and serves its own metrics. Default: "+DefaultPrometheusEndpoint).
		StringVar(&cfg.Prometheus.Endpoint)

	a.Flag("admin.listen-address", "Administrative HTTP address this process listens on. Default: "+DefaultAdminListenAddress).
		StringVar(&cfg.Admin.ListenAddress)

	a.Flag("security.root-certificate", "Root CA certificate to use for TLS connections, in PEM format (e.g., root.crt). May be repeated.").
		StringsVar(&cfg.Security.RootCertificates)

	a.Flag("opentelemetry.metrics-prefix", "Customized prefix for exporter metrics. If not set, none will be used").
		StringVar(&cfg.OpenTelemetry.MetricsPrefix)

	a.Flag("opentelemetry.use-meta-labels", "Prometheus target labels prefixed with __meta_ map into labels.").
		BoolVar(&cfg.OpenTelemetry.UseMetaLabels)

	a.Flag("include", "PromQL metric and label matcher which must pass for a series to be forwarded to OpenTelemetry. If repeated, the series must pass any of the filter sets to be forwarded.").
		StringsVar(&cfg.Filtersets)

	a.Flag("startup.delay", "Delay at startup to allow Prometheus its initial scrape. Default: "+DefaultStartupDelay.String()).
		DurationVar(&cfg.StartupDelay.Duration)

	a.Flag(promlogflag.LevelFlagName, promlogflag.LevelFlagHelp).StringVar(&cfg.LogConfig.Level)
	a.Flag(promlogflag.FormatFlagName, promlogflag.FormatFlagHelp).StringVar(&cfg.LogConfig.Format)

	_, err := a.Parse(args[1:])
	if err != nil {
		return MainConfig{}, nil, nil,
			errors.Wrap(err, "error parsing command-line arguments")
	}

	var (
		metricRenames  map[string]string
		staticMetadata []*metadata.Entry
	)

	if cfg.ConfigFilename != "" {
		data, err := readFunc(cfg.ConfigFilename)
		if err != nil {
			return MainConfig{}, nil, nil,
				errors.Wrap(err, "reading file")
		}

		metricRenames, staticMetadata, err = parseConfigFile(data, &cfg)
		if err != nil {
			return MainConfig{}, nil, nil,
				errors.Wrap(err, "error parsing configuration file")
		}

		// Re-parse the command-line flags to let the
		// command-line arguments take precedence.
		_, err = a.Parse(args[1:])
		if err != nil {
			return MainConfig{}, nil, nil,
				errors.Wrap(err, "error re-parsing command-line arguments")
		}
	}

	if err := checkEmptyKeys("destination attribute", cfg.Destination.Attributes); err != nil {
		return MainConfig{}, nil, nil, err
	}
	if err := checkEmptyKeys("destination header", cfg.Destination.Headers); err != nil {
		return MainConfig{}, nil, nil, err
	}

	return cfg, metricRenames, staticMetadata, nil
}

func main() {
	if os.Getenv("DEBUG") != "" {
		runtime.SetBlockProfileRate(20)
		runtime.SetMutexProfileFraction(20)
	}

	cfg, metricRenames, staticMetadata, err := Configure(os.Args, ioutil.ReadFile)
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
		name  string
		value string
	}
	for _, pair := range []namedURL{
		{"destination.endpoint", cfg.Destination.Endpoint},
		{"prometheus.endpoint", cfg.Prometheus.Endpoint},
	} {
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

	grpcMetadata, err := buildGRPCHeaders(cfg.Destination.Headers)
	if err != nil {
		level.Error(logger).Log("msg", "could not parse --grpc.header", "err", err)
		os.Exit(2)
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
		// TODO: Re-enable HTTP tracing:
		// Transport: otelhttp.NewTransport(http.DefaultTransport),
	}

	// for _, backend := range cfg.MonitoringBackends {
	// 	switch backend {
	// 	case "prometheus":
	// 		promExporter, err := oc_prometheus.NewExporter(oc_prometheus.Options{
	// 			Registry: prometheus.DefaultRegisterer.(*prometheus.Registry),
	// 		})
	// 		if err != nil {
	// 			level.Error(logger).Log("msg", "Creating Prometheus exporter failed", "err", err)
	// 			os.Exit(1)
	// 		}
	// 		view.RegisterExporter(promExporter)
	// 	default:
	// 		level.Error(logger).Log("msg", "Unknown monitoring backend", "backend", backend)
	// 		os.Exit(1)
	// 	}
	// }

	filtersets, err := parseFiltersets(logger, cfg.Filtersets)
	if err != nil {
		level.Error(logger).Log("msg", "error parsing --include", "err", err)
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
	config.DefaultQueueConfig.MaxSamplesPerSend = otlp.MaxTimeseriesesPerRequest
	// We want the queues to have enough buffer to ensure consistent flow with full batches
	// being available for every new request.
	// Testing with different latencies and shard numbers have shown that 3x of the batch size
	// works well.
	config.DefaultQueueConfig.Capacity = 3 * otlp.MaxTimeseriesesPerRequest

	outputURL, _ := url.Parse(cfg.Destination.Endpoint)

	var scf otlp.StorageClientFactory = &otlpClientFactory{
		logger:   log.With(logger, "component", "storage"),
		url:      outputURL,
		timeout:  10 * time.Second,
		security: cfg.Security,
		headers:  grpcMetadata,
	}

	queueManager, err := otlp.NewQueueManager(
		log.With(logger, "component", "queue_manager"),
		config.DefaultQueueConfig,
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
		filtersets,
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

func buildGRPCHeaders(values map[string]string) (grpcMetadata.MD, error) {
	return grpcMetadata.New(values), nil
}

func checkEmptyKeys(kind string, values map[string]string) error {
	for key, _ := range values {
		if key == "" {
			return fmt.Errorf("empty %s key(s)", kind)
		}
	}
	return nil
}

type otlpClientFactory struct {
	logger   log.Logger
	url      *url.URL
	timeout  time.Duration
	security SecurityConfig
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

// parseFiltersets parses two flags that contain PromQL-style metric/label selectors and
// returns a list of the resulting matchers.
func parseFiltersets(logger log.Logger, filtersets []string) ([][]*labels.Matcher, error) {
	var matchers [][]*labels.Matcher
	for _, f := range filtersets {
		m, err := parser.ParseMetricSelector(f)
		if err != nil {
			return nil, errors.Errorf("cannot parse --include flag '%s': %q", f, err)
		}
		matchers = append(matchers, m)
	}
	return matchers, nil
}

func parseConfigFile(data []byte, cfg *MainConfig) (map[string]string, []*metadata.Entry, error) {
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, nil, errors.Wrap(err, "invalid YAML")
	}
	return processMainConfig(cfg)
}

func processMainConfig(cfg *MainConfig) (map[string]string, []*metadata.Entry, error) {
	renameMapping := map[string]string{}
	for _, r := range cfg.MetricRenames {
		renameMapping[r.From] = r.To
	}
	staticMetadata := []*metadata.Entry{}
	for _, sm := range cfg.StaticMetadata {
		switch sm.Type {
		case metadata.MetricTypeUntyped:
			// Convert "untyped" to the "unknown" type used internally as of Prometheus 2.5.
			sm.Type = textparse.MetricTypeUnknown
		case textparse.MetricTypeCounter, textparse.MetricTypeGauge, textparse.MetricTypeHistogram,
			textparse.MetricTypeSummary, textparse.MetricTypeUnknown:
		default:
			return nil, nil, errors.Errorf("invalid metric type %q", sm.Type)
		}
		var valueType metadata.ValueType
		switch sm.ValueType {
		case "double", "":
			valueType = metadata.DOUBLE
		case "int64":
			valueType = metadata.INT64
		default:
			return nil, nil, errors.Errorf("invalid value type %q", sm.ValueType)
		}
		staticMetadata = append(
			staticMetadata,
			&metadata.Entry{
				Metric:     sm.Metric,
				MetricType: textparse.MetricType(sm.Type),
				ValueType:  valueType,
				Help:       sm.Help,
			},
		)
	}
	return renameMapping, staticMetadata, nil
}

func usage(err error) {
	fmt.Fprintf(
		os.Stderr,
		"run '%s --help' for usage and configuration syntax: %v\n",
		os.Args[0],
		err,
	)
}

func (d *DurationConfig) UnmarshalJSON(data []byte) error {
	if d == nil {
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

func (d DurationConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Duration.String())
}
