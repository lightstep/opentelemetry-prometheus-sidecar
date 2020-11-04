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

const (
	defaultStartupDelay = time.Minute

	briefDescription = `
The OpenTelemetry Prometheus sidecar runs alongside the
Prometheus (https://prometheus.io/) Server and sends metrics data to
an OpenTelemetry (https://opentelemetry.io) Protocol endpoint.
`
)

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

type metricRenamesConfig struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type staticMetadataConfig struct {
	Metric    string `json:"metric"`
	Type      string `json:"type"`
	ValueType string `json:"value_type"`
	Help      string `json:"help"`
}

type securityConfig struct {
	// @@@ TODO should be []string
	RootCertificate string `json:"root_certificate"`
}

type durationConfig struct {
	time.Duration `yaml:"-,inline"`
}

type otlpConfig struct {
	Attributes map[string]string `json:"attributes"`
	Endpoint   string            `json:"endpoint"`
	Headers    map[string]string `json:"headers"`
}

type logConfig struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

type promConfig struct {
	WAL      string `json:"wal"`
	Endpoint string `json:"endpoint"`
}

type otelConfig struct {
	MetricsPrefix string `json:"metrics_prefix"`
}

type mainConfig struct {
	// General
	ConfigFilename string         `json:"-"`
	Security       securityConfig `json:"security"`
	ListenAddress  string         `json:"listen_address"`
	StartupDelay   durationConfig `json:"startup_delay"`

	// Prometheus input
	Prometheus promConfig `json:"prometheus"`

	// Opentelemetry setup
	OpenTelemetry otelConfig `json:"opentelemetry"`

	// Primary output
	Destination otlpConfig `json:"destination"`

	// Diagnostic output
	LogConfig logConfig `json:"log_config"`

	// Sidecar customization
	Filtersets     []string               `json:"filter_sets"`
	MetricRenames  []metricRenamesConfig  `json:"metric_renames"`
	StaticMetadata []staticMetadataConfig `json:"static_metadata"`
	UseMetaLabels  bool                   `json:"use_meta_labels"`
}

func main() {
	var cfg mainConfig

	if os.Getenv("DEBUG") != "" {
		runtime.SetBlockProfileRate(20)
		runtime.SetMutexProfileFraction(20)
	}

	a := kingpin.New(filepath.Base(os.Args[0]), briefDescription)

	a.Version(version.Print("opentelemetry-prometheus-sidecar"))

	a.HelpFlag.Short('h')

	a.Flag("config-file", "A configuration file.").
		Default("").StringVar(&cfg.ConfigFilename)

	a.Flag("destination.endpoint", "Address of the OpenTelemetry Metrics protocol (gRPC) endpoint (e.g., https://host:port).  Use \"http\" (not \"https\") for an insecure connection.").
		Default("").StringVar(&cfg.Destination.Endpoint)

	a.Flag("opentelemetry.metrics-prefix", "Customized prefix for exporter metrics. If not set, none will be used").
		Default("").StringVar(&cfg.OpenTelemetry.MetricsPrefix)

	a.Flag("prometheus.wal", "Directory from where to read the Prometheus TSDB WAL.").
		Default("data/wal").StringVar(&cfg.Prometheus.WAL)

	a.Flag("prometheus.endpoint", "Endpoint where Prometheus hosts its  UI, API, and serves its own metrics.").
		Default("http://127.0.0.1:9090/").StringVar(&cfg.Prometheus.Endpoint)

	a.Flag("web.listen-address", "Address this process listens on.  Note: there is nothing to see here. TODO(healthcheck)").
		Default("0.0.0.0:9091").StringVar(&cfg.ListenAddress)

	a.Flag("include", "PromQL metric and label matcher which must pass for a series to be forwarded to OpenTelemetry. If repeated, the series must pass any of the filter sets to be forwarded.").
		StringsVar(&cfg.Filtersets)

	a.Flag("security.root-certificate", "Root CA certificate to use for TLS connections, in PEM format (e.g., root.crt).").
		Default("").StringVar(&cfg.Security.RootCertificate)

	a.Flag("destination.header", "Headers for gRPC connection (e.g., MyHeader=Value1). May be repeated.").
		StringMapVar(&cfg.Destination.Headers)

	a.Flag("destination.attribute", "Attributes for exported metrics (e.g., MyResource=Value1). May be repeated.").
		StringMapVar(&cfg.Destination.Attributes)

	a.Flag("resource.use-meta-labels", "Prometheus target labels prefixed with __meta_ map into labels.").
		BoolVar(&cfg.UseMetaLabels)

	a.Flag("startup.delay", "Delay at startup to allow Prometheus its initial scrape").
		Default(defaultStartupDelay.String()).DurationVar(&cfg.StartupDelay.Duration)

	var plc promlog.Config
	promlogflag.AddFlags(a, &plc)

	_, err := a.Parse(os.Args[1:])
	if err != nil {
		usage("error parsing command-line arguments", err)
		os.Exit(2)
	}

	cfg.LogConfig.Level = plc.Level.String()
	cfg.LogConfig.Format = plc.Format.String()

	var (
		metricRenames  map[string]string
		staticMetadata []*metadata.Entry
	)

	if cfg.ConfigFilename != "" {
		var fromFile mainConfig
		metricRenames, staticMetadata, err = parseConfigFile(cfg.ConfigFilename, &fromFile)
		if err != nil {
			usage("error parsing configuration file", err)
			os.Exit(2)
		}
		fmt.Println("BEFORE", cfg)
		fmt.Println("READ url", fromFile.Prometheus.Endpoint)

		cfg = fromFile

		// Re-parse the command-line flags to let the
		// command-line have precedence.
		fmt.Println("Re-parsing:", os.Args[1:])
		_, err := a.Parse(os.Args[1:])
		if err != nil {
			usage("error re-parsing command-line arguments", err)
			os.Exit(2)
		}

		fmt.Println("AFTER", cfg)
	}

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

	if err := parseResourceAttributes(cfg.Destination.Attributes); err != nil {
		level.Error(logger).Log("msg", "error parsing --resource.attribute", "err", err)
		os.Exit(2)
	}

	targetCache := targets.NewCache(
		logger,
		httpClient,
		targetsURL,
		labels.FromMap(cfg.Destination.Attributes),
		cfg.UseMetaLabels,
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

	// Exclude kingpin default flags to expose only Prometheus ones.
	boilerplateFlags := kingpin.New("", "").Version("")
	for _, f := range a.Model().Flags {
		if boilerplateFlags.GetFlag(f.Name) != nil {
			continue
		}

	}

	// Monitor outgoing connections on default transport with conntrack.
	http.DefaultTransport.(*http.Transport).DialContext = conntrack.NewDialContextFunc(
		conntrack.DialWithTracing(),
	)

	// // TODO: this should be _if_ the prom monitoring is selected
	// http.Handle("/metrics", promhttp.Handler())

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
			Addr: cfg.ListenAddress,
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
	// TODO: sanitize key names?
	return grpcMetadata.New(values), nil
}

func parseResourceAttributes(values map[string]string) error {
	// TODO: sanitize key names?
	return nil
}

type otlpClientFactory struct {
	logger   log.Logger
	url      *url.URL
	timeout  time.Duration
	security securityConfig
	headers  grpcMetadata.MD
}

func (s *otlpClientFactory) New() otlp.StorageClient {
	return otlp.NewClient(&otlp.ClientConfig{
		Logger:          s.logger,
		URL:             s.url,
		Timeout:         s.timeout,
		RootCertificate: s.security.RootCertificate,
		Headers:         s.headers,
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

func parseConfigFile(filename string, cfg *mainConfig) (map[string]string, []*metadata.Entry, error) {
	b, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, nil, errors.Wrap(err, "reading file")
	}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, nil, errors.Wrap(err, "invalid YAML")
	}
	return processMainConfig(cfg)
}

func processMainConfig(cfg *mainConfig) (map[string]string, []*metadata.Entry, error) {
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

func usage(desc string, err error) {
	fmt.Fprintf(
		os.Stderr,
		"%s: run '%s --help' for usage and configuration syntax: %v\n",
		desc,
		os.Args[0],
		err,
	)
}

func (d *durationConfig) UnmarshalJSON(data []byte) error {
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

func (d durationConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Duration.String())
}
