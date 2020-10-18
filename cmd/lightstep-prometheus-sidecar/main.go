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
	"strings"
	"syscall"
	"time"

	oc_prometheus "contrib.go.opencensus.io/exporter/prometheus"
	"github.com/ghodss/yaml"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/lightstep-prometheus-sidecar/metadata"
	"github.com/lightstep/lightstep-prometheus-sidecar/otlp"
	"github.com/lightstep/lightstep-prometheus-sidecar/retrieval"
	"github.com/lightstep/lightstep-prometheus-sidecar/tail"
	"github.com/lightstep/lightstep-prometheus-sidecar/targets"
	conntrack "github.com/mwitkow/go-conntrack"
	"github.com/oklog/oklog/pkg/group"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promlog"
	promlogflag "github.com/prometheus/common/promlog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/prometheus/promql"
	"go.opencensus.io/plugin/ocgrpc"
	"go.opencensus.io/plugin/ochttp"
	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	grpcMetadata "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/resolver/manual"
	kingpin "gopkg.in/alecthomas/kingpin.v2"
)

var (
	sizeDistribution    = view.Distribution(0, 1024, 2048, 4096, 16384, 65536, 262144, 1048576, 4194304, 33554432)
	latencyDistribution = view.Distribution(0, 1, 2, 5, 10, 15, 25, 50, 100, 200, 400, 800, 1500, 3000, 6000)

	// VersionTag identifies the version of this binary.
	VersionTag = tag.MustNewKey("version")
	// UptimeMeasure is a cumulative metric.
	UptimeMeasure = stats.Int64(
		"agent.googleapis.com/agent/uptime",
		"uptime of the OpenTelemetry Prometheus collector",
		stats.UnitSeconds)
)

func init() {
	prometheus.MustRegister(version.NewCollector("prometheus"))

	if err := view.Register(
		&view.View{
			Name:        "opencensus.io/http/client/request_count",
			Description: "Count of HTTP requests started",
			Measure:     ochttp.ClientRequestCount,
			TagKeys:     []tag.Key{ochttp.Method, ochttp.Path},
			Aggregation: view.Count(),
		},
		&view.View{
			Name:        "opencensus.io/http/client/request_bytes",
			Description: "Size distribution of HTTP request body",
			Measure:     ochttp.ClientRequestBytes,
			TagKeys:     []tag.Key{ochttp.Method, ochttp.StatusCode, ochttp.Path},
			Aggregation: sizeDistribution,
		},
		&view.View{
			Name:        "opencensus.io/http/client/response_bytes",
			Description: "Size distribution of HTTP response body",
			Measure:     ochttp.ClientResponseBytes,
			TagKeys:     []tag.Key{ochttp.Method, ochttp.StatusCode, ochttp.Path},
			Aggregation: sizeDistribution,
		},
		&view.View{
			Name:        "opencensus.io/http/client/latency",
			Description: "Latency distribution of HTTP requests",
			TagKeys:     []tag.Key{ochttp.Method, ochttp.StatusCode, ochttp.Path},
			Measure:     ochttp.ClientLatency,
			Aggregation: latencyDistribution,
		},
	); err != nil {
		panic(err)
	}
	if err := view.Register(
		ocgrpc.DefaultClientViews...,
	); err != nil {
		panic(err)
	}
	if err := view.Register(
		&view.View{
			Measure:     UptimeMeasure,
			TagKeys:     []tag.Key{VersionTag},
			Aggregation: view.Sum(),
		},
	); err != nil {
		panic(err)
	}
}

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

type fileConfig struct {
	MetricRenames  []metricRenamesConfig  `json:"metric_renames"`
	StaticMetadata []staticMetadataConfig `json:"static_metadata"`
}

type securityConfig struct {
	ServerCertificate string `json:"server_certificate"`
}

type grpcConfig struct {
	Headers []string `json:"headers"`
}

type resourceConfig struct {
	Attributes    []string `json:"attributes"`
	UseMetaLabels bool     `json:"use_meta_labels"`
}

type mainConfig struct {
	ConfigFilename       string
	OpenTelemetryAddress *url.URL
	MetricsPrefix        string
	WALDirectory         string
	PrometheusURL        *url.URL
	ListenAddress        string
	Filtersets           []string
	MetricRenames        map[string]string
	StaticMetadata       []*metadata.Entry
	manualResolver       *manual.Resolver
	MonitoringBackends   []string
	PromlogConfig        promlog.Config
	Security             securityConfig
	GRPC                 grpcConfig
	Resource             resourceConfig
}

func main() {
	if os.Getenv("DEBUG") != "" {
		runtime.SetBlockProfileRate(20)
		runtime.SetMutexProfileFraction(20)
	}

	var cfg mainConfig

	a := kingpin.New(filepath.Base(os.Args[0]), "The Prometheus monitoring server")

	a.Version(version.Print("prometheus"))

	a.HelpFlag.Short('h')

	a.Flag("config-file", "A configuration file.").StringVar(&cfg.ConfigFilename)

	a.Flag("opentelemetry.api-address", "Address of the OpenTelemetry Metrics API.").
		Default("").URLVar(&cfg.OpenTelemetryAddress)

	a.Flag("opentelemetry.metrics-prefix", "Customized prefix for exporter metrics. If not set, none will be used").
		StringVar(&cfg.MetricsPrefix)

	a.Flag("prometheus.wal-directory", "Directory from where to read the Prometheus TSDB WAL.").
		Default("data/wal").StringVar(&cfg.WALDirectory)

	a.Flag("prometheus.api-address", "Address to listen on for UI, API, and telemetry.  Use ?auth=false for an insecure connection.").
		Default("http://127.0.0.1:9090/").URLVar(&cfg.PrometheusURL)

	a.Flag("monitoring.backend", "Monitoring backend(s) for internal metrics").Default("prometheus").
		EnumsVar(&cfg.MonitoringBackends, "prometheus")

	a.Flag("web.listen-address", "Address to listen on for UI, API, and telemetry.").
		Default("0.0.0.0:9091").StringVar(&cfg.ListenAddress)

	a.Flag("include", "PromQL metric and label matcher which must pass for a series to be forwarded to OpenTelemetry. If repeated, the series must pass any of the filter sets to be forwarded.").
		StringsVar(&cfg.Filtersets)

	a.Flag("security.server-certificate", "Public certificate for the server to use for TLS connections (e.g., server.crt, in pem format).").
		StringVar(&cfg.Security.ServerCertificate)

	// TODO: Cover the two flags below in the end-to-end test.
	a.Flag("grpc.header", "Headers for gRPC connection (e.g., MyHeader=Value1). May be repeated.").
		StringsVar(&cfg.GRPC.Headers)

	a.Flag("resource.attribute", "Attributes for exported metrics (e.g., MyResource=Value1). May be repeated.").
		StringsVar(&cfg.Resource.Attributes)

	a.Flag("resource.use-meta-labels", "Prometheus target labels prefixed with __meta_ map into labels.").
		BoolVar(&cfg.Resource.UseMetaLabels)

	promlogflag.AddFlags(a, &cfg.PromlogConfig)

	_, err := a.Parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, errors.Wrapf(err, "Error parsing commandline arguments"))
		a.Usage(os.Args[1:])
		os.Exit(2)
	}

	logger := promlog.New(&cfg.PromlogConfig)
	if cfg.ConfigFilename != "" {
		cfg.MetricRenames, cfg.StaticMetadata, err = parseConfigFile(cfg.ConfigFilename)
		if err != nil {
			msg := fmt.Sprintf("Parse config file %s", cfg.ConfigFilename)
			level.Error(logger).Log("msg", msg, "err", err)
			os.Exit(2)
		}
	}

	level.Info(logger).Log("msg", "Starting OpenTelemetry Prometheus sidecar", "version", version.Info())
	level.Info(logger).Log("build_context", version.BuildContext())
	level.Info(logger).Log("host_details", Uname())
	level.Info(logger).Log("fd_limits", FdLimits())

	grpcHeadersMap := map[string]string{}
	for _, hdr := range cfg.GRPC.Headers {
		kvs := strings.SplitN(hdr, "=", 2)
		if len(kvs) != 2 {
			level.Error(logger).Log("msg", "grpc header should have key=value syntax", "ex", hdr)
			os.Exit(2)
		}
		grpcHeadersMap[kvs[0]] = kvs[1]
	}
	grpcHeaders := grpcMetadata.New(grpcHeadersMap)

	// We instantiate a context here since the tailer is used by two other components.
	// The context will be used in the lifecycle of prometheusReader further down.
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		uptimeUpdateTime := time.Now()
		c := time.Tick(60 * time.Second)
		for now := range c {
			stats.RecordWithTags(ctx,
				[]tag.Mutator{tag.Upsert(VersionTag, fmt.Sprintf("opentelemetry-prometheus-sidecar/%s", version.Version))},
				UptimeMeasure.M(int64(now.Sub(uptimeUpdateTime).Seconds())))
			uptimeUpdateTime = now
		}
	}()

	httpClient := &http.Client{Transport: &ochttp.Transport{}}

	for _, backend := range cfg.MonitoringBackends {
		switch backend {
		case "prometheus":
			promExporter, err := oc_prometheus.NewExporter(oc_prometheus.Options{
				Registry: prometheus.DefaultRegisterer.(*prometheus.Registry),
			})
			if err != nil {
				level.Error(logger).Log("msg", "Creating Prometheus exporter failed", "err", err)
				os.Exit(1)
			}
			view.RegisterExporter(promExporter)
		default:
			level.Error(logger).Log("msg", "Unknown monitoring backend", "backend", backend)
			os.Exit(1)
		}
	}

	filtersets, err := parseFiltersets(logger, cfg.Filtersets)
	if err != nil {
		level.Error(logger).Log("msg", "Error parsing --include", "err", err)
		os.Exit(2)
	}

	targetsURL, err := cfg.PrometheusURL.Parse(targets.DefaultAPIEndpoint)
	if err != nil {
		panic(err)
	}

	resAttrMap := map[string]string{}
	for _, attr := range cfg.Resource.Attributes {
		kvs := strings.SplitN(attr, "=", 2)
		if len(kvs) != 2 {
			level.Error(logger).Log("msg", "resource attribute should have key=value syntax", "ex", attr)
			os.Exit(2)
		}
		resAttrMap[kvs[0]] = kvs[1]
	}

	targetCache := targets.NewCache(
		logger,
		httpClient,
		targetsURL,
		labels.FromMap(resAttrMap),
		cfg.Resource.UseMetaLabels,
	)

	metadataURL, err := cfg.PrometheusURL.Parse(metadata.DefaultEndpointPath)
	if err != nil {
		panic(err)
	}
	metadataCache := metadata.NewCache(httpClient, metadataURL, cfg.StaticMetadata)

	tailer, err := tail.Tail(ctx, cfg.WALDirectory)
	if err != nil {
		level.Error(logger).Log("msg", "Tailing WAL failed", "err", err)
		os.Exit(1)
	}
	config.DefaultQueueConfig.MaxSamplesPerSend = otlp.MaxTimeseriesesPerRequest
	// We want the queues to have enough buffer to ensure consistent flow with full batches
	// being available for every new request.
	// Testing with different latencies and shard numbers have shown that 3x of the batch size
	// works well.
	config.DefaultQueueConfig.Capacity = 3 * otlp.MaxTimeseriesesPerRequest

	var scf otlp.StorageClientFactory = &otlpClientFactory{
		logger:         log.With(logger, "component", "storage"),
		url:            cfg.OpenTelemetryAddress,
		timeout:        10 * time.Second,
		manualResolver: cfg.manualResolver,
		security:       cfg.Security,
		headers:        grpcHeaders,
	}

	queueManager, err := otlp.NewQueueManager(
		log.With(logger, "component", "queue_manager"),
		config.DefaultQueueConfig,
		scf,
		tailer,
	)
	if err != nil {
		level.Error(logger).Log("msg", "Creating queue manager failed", "err", err)
		os.Exit(1)
	}

	prometheusReader := retrieval.NewPrometheusReader(
		log.With(logger, "component", "Prometheus reader"),
		cfg.WALDirectory,
		tailer,
		filtersets,
		cfg.MetricRenames,
		targetCache,
		metadataCache,
		queueManager,
		cfg.MetricsPrefix,
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

	// TODO: this should be _if_ the prom monitoring is selected
	http.Handle("/metrics", promhttp.Handler())

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
				startOffset, err := retrieval.ReadProgressFile(cfg.WALDirectory)
				if err != nil {
					level.Warn(logger).Log("msg", "reading progress file failed", "err", err)
					startOffset = 0
				}
				// Write the file again once to ensure we have write permission on startup.
				if err := retrieval.SaveProgressFile(cfg.WALDirectory, startOffset); err != nil {
					return err
				}
				waitForPrometheus(ctx, logger, cfg.PrometheusURL)
				// Sleep a fixed amount of time to allow the first scrapes to complete.
				select {
				case <-time.After(time.Minute):
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

type otlpClientFactory struct {
	logger         log.Logger
	url            *url.URL
	timeout        time.Duration
	manualResolver *manual.Resolver
	security       securityConfig
	headers        grpcMetadata.MD
}

func (s *otlpClientFactory) New() otlp.StorageClient {
	return otlp.NewClient(&otlp.ClientConfig{
		Logger:   s.logger,
		URL:      s.url,
		Timeout:  s.timeout,
		Resolver: s.manualResolver,
		CertFile: s.security.ServerCertificate,
		Headers:  s.headers,
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
		m, err := promql.ParseMetricSelector(f)
		if err != nil {
			return nil, errors.Errorf("cannot parse --include flag '%s': %q", f, err)
		}
		matchers = append(matchers, m)
	}
	return matchers, nil
}

func parseConfigFile(filename string) (map[string]string, []*metadata.Entry, error) {
	b, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, nil, errors.Wrap(err, "reading file")
	}
	var fc fileConfig
	if err := yaml.Unmarshal(b, &fc); err != nil {
		return nil, nil, errors.Wrap(err, "invalid YAML")
	}
	return processFileConfig(fc)
}

func processFileConfig(fc fileConfig) (map[string]string, []*metadata.Entry, error) {
	renameMapping := map[string]string{}
	for _, r := range fc.MetricRenames {
		renameMapping[r.From] = r.To
	}
	staticMetadata := []*metadata.Entry{}
	for _, sm := range fc.StaticMetadata {
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
		staticMetadata = append(staticMetadata,
			&metadata.Entry{Metric: sm.Metric, MetricType: textparse.MetricType(sm.Type), ValueType: valueType, Help: sm.Help})
	}
	return renameMapping, staticMetadata, nil
}
