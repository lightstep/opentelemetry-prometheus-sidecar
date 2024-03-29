// Copyright The OpenTelemetry Authors
//
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

package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/ghodss/yaml"
	"github.com/go-kit/kit/log"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/snappy"
	"github.com/pkg/errors"
	"github.com/prometheus/common/model"
	promlogflag "github.com/prometheus/common/promlog/flag"
	"github.com/prometheus/common/version"
	promconfig "github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/pkg/textparse"
	"go.opentelemetry.io/otel/attribute"
	"gopkg.in/alecthomas/kingpin.v2"
)

const (
	DefaultAdminPort          = 9091
	DefaultAdminListenIP      = "0.0.0.0"
	DefaultPrometheusEndpoint = "http://127.0.0.1:9090/"
	DefaultWALDirectory       = "data/wal"

	DefaultExportTimeout             = time.Second * 60
	DefaultHealthCheckTimeout        = time.Second * 5
	DefaultHealthCheckPeriod         = time.Second * 60
	DefaultHealthCheckThresholdRatio = 0.5
	DefaultReadinessPeriod           = time.Second * 5
	DefaultScrapeIntervalWaitPeriod  = time.Minute
	DefaultMaxPointAge               = time.Hour * 25
	DefaultShutdownDelay             = time.Minute
	DefaultStartupTimeout            = time.Minute * 10
	DefaultNoisyLogPeriod            = time.Second * 60
	DefaultPrometheusTimeout         = time.Second * 60

	DefaultSingleMetricBatchSizeLimit = 4096

	DefaultSupervisorBufferSize  = 16384
	DefaultSupervisorLogsHistory = 16

	// How many bytes per request
	DefaultMaxBytesPerRequest = 65536

	// How many items can be queued before the reader blocks
	//
	// Note: queue entries are 16 bytes, this is a large block of memory.
	// TODO: Consider adjusting this down after understanding performance
	// of the single-queue approach to sharding taken in #247.
	DefaultQueueSize = 100000

	// Min number of shards, i.e. amount of concurrency
	DefaultMinShards = 1
	// Max number of shards, i.e. amount of concurrency
	DefaultMaxShards = 200

	// TODO: This was 1 minute; it's not clear how often it should
	// happen bit it's a suspiciously short timeout given that it
	// can race with startup.  Should we wait until the WAL reader
	// reaches a current position before we begin garbage
	// collection?
	DefaultSeriesCacheGarbageCollectionPeriod = time.Minute * 15

	// DefaultSeriesCacheLookupPeriod determines how often the
	// sidecar will try to load metadata for the series when it is
	// not known.
	DefaultSeriesCacheLookupPeriod = time.Minute * 3

	// TODO: The setting below is not configurable, it should be.

	// DefaultMaxExportAttempts sets a maximum on the number of
	// attempts to export a request.  This is not RPC requests,
	// but attempts, defined as trying for up to at least the
	// export timeout.  This helps in case a request fails
	// repeatedly, in which case the queue could block the WAL
	// reader.
	DefaultMaxExportAttempts = 2

	DefaultMaxRetrySkipSegments = 5

	// DefaultCheckpointInProgressPeriod is the maximum amount of time
	// to wait if it appears a checkpoint is in progress.
	DefaultCheckpointInProgressPeriod = time.Minute * 5

	briefDescription = `
The OpenTelemetry Prometheus sidecar runs alongside the
Prometheus (https://prometheus.io/) Server and sends metrics data to
an OpenTelemetry (https://opentelemetry.io) Protocol endpoint.
`

	AgentKey = "telemetry-reporting-agent"

	// Some metric names are shared across packages, for healthchecking.

	SidecarPrefix = "sidecar."

	SeriesDefinedMetric = "sidecar.series.defined"
	DroppedSeriesMetric = "sidecar.series.dropped"
	CurrentSeriesMetric = "sidecar.series.current"

	OutcomeMetric = "sidecar.queue.outcome"

	ProducedPointsMetric = "sidecar.points.produced"
	DroppedPointsMetric  = "sidecar.points.dropped"
	SkippedPointsMetric  = "sidecar.points.skipped"

	FailingMetricsMetric = "sidecar.metrics.failing"
	CurrentMetricsMetric = "sidecar.metrics.current"

	LeadershipMetric = "sidecar.leadership"

	OutcomeKey          = attribute.Key("outcome")
	OutcomeSuccessValue = "success"

	HealthCheckURI = "/-/health"

	// PrometheusCurrentSegmentMetricName names an internal gauge
	// exposed by Prometheus (having no attributes).
	PrometheusCurrentSegmentMetricName = "prometheus_tsdb_wal_segment_current"

	// PrometheusTargetIntervalLengthName is an internal histogram
	// indicating how long the interval between scrapes.
	PrometheusTargetIntervalLengthName = "prometheus_target_interval_length_seconds"

	// PrometheusBuildInfoName provides prometheus version information
	PrometheusBuildInfoName = "prometheus_build_info"
	// PrometheusMinVersion is the minimum supported version
	PrometheusMinVersion = "2.10.0"

	// LeaderLockDefaultNamespace is the name of the default k8s namespace.
	// Note: can't be empty.
	LeaderLockDefaultNamespace = "default"

	// LeaderLockDefaultName will be used when no `prometheus`
	// external label is found.
	LeaderLockDefaultName = "otel-prom-sidecar"
)

var (
	AgentMainValue = fmt.Sprint(
		"opentelemetry-prometheus-sidecar-main/",
		version.Version,
	)
	AgentSecondaryValue = fmt.Sprint(
		"opentelemetry-prometheus-sidecar-telemetry/",
		version.Version,
	)
	AgentSupervisorValue = fmt.Sprint(
		"opentelemetry-prometheus-sidecar-supervisor/",
		version.Version,
	)
)

type MetricRenamesConfig struct {
	From string `json:"from"`
	To   string `json:"to"`
}
type StaticMetadataConfig struct {
	Metric    string               `json:"metric"`
	Type      textparse.MetricType `json:"type"`
	ValueType string               `json:"value_type"`
	Help      string               `json:"help"`
}

type SecurityConfig struct {
	RootCertificates []string `json:"root_certificates"`
}

type DurationConfig struct {
	time.Duration `json:"duration" yaml:"-,inline"`
}

type OTLPConfig struct {
	Endpoint    string            `json:"endpoint"`
	Headers     map[string]string `json:"headers"`
	Attributes  map[string]string `json:"attributes"`
	Timeout     DurationConfig    `json:"timeout"`
	Compression string            `json:"compression"`
}

func (config OTLPConfig) Copy() OTLPConfig {
	rv := config

	headers := map[string]string{}
	for key, value := range config.Headers {
		headers[key] = value
	}
	rv.Headers = headers

	attrs := map[string]string{}
	for key, value := range config.Attributes {
		attrs[key] = value
	}
	rv.Attributes = attrs

	return rv
}

type LogConfig struct {
	Level   string `json:"level"`
	Format  string `json:"format"`
	Verbose int    `json:"verbose"`
}

type PromConfig struct {
	Endpoint                  string         `json:"endpoint"`
	WAL                       string         `json:"wal"`
	MaxPointAge               DurationConfig `json:"max_point_age"`
	HealthCheckRequestTimeout DurationConfig `json:"health_check_request_timeout"`
}

type OTelConfig struct {
	MaxBytesPerRequest int    `json:"max_bytes_per_request"`
	MetricsPrefix      string `json:"metrics_prefix"`
	MinShards          int    `json:"min_shards"`
	MaxShards          int    `json:"max_shards"`
	QueueSize          int    `json:"queue_size"`
}

type AdminConfig struct {
	ListenIP                  string         `json:"listen_ip"`
	Port                      int            `json:"port"`
	HealthCheckPeriod         DurationConfig `json:"health_check_period"`
	HealthCheckThresholdRatio float64        `json:"health_check_threshold_ratio"`
}

type K8SLeaderElectionConfig struct {
	Namespace string `json:"namespace"`
}

type LeaderElectionConfig struct {
	Enabled bool `json:"enabled"`

	K8S K8SLeaderElectionConfig `json:"k8s"`
}

type MainConfig struct {
	// Note: These fields are ordered so that JSON and YAML
	// marshal in order of importance.

	Destination    OTLPConfig             `json:"destination"`
	Prometheus     PromConfig             `json:"prometheus"`
	OpenTelemetry  OTelConfig             `json:"opentelemetry"`
	Admin          AdminConfig            `json:"admin"`
	Security       SecurityConfig         `json:"security"`
	Diagnostics    OTLPConfig             `json:"diagnostics"`
	StartupTimeout DurationConfig         `json:"startup_timeout"`
	Filters        []string               `json:"filters"`
	MetricRenames  []MetricRenamesConfig  `json:"metric_renames"`
	StaticMetadata []StaticMetadataConfig `json:"static_metadata"`
	LogConfig      LogConfig              `json:"log"`
	LeaderElection LeaderElectionConfig   `json:"leader_election"`

	DisableSupervisor  bool `json:"disable_supervisor"`
	DisableDiagnostics bool `json:"disable_diagnostics"`

	// This field cannot be parsed inside a configuration file,
	// only can be set by command-line flag.:
	ConfigFilename string `json:"-" yaml:"-"`
}

// TODO Remove this code. Stop using promconfig.QueueConfig.
func (c MainConfig) QueueConfig() promconfig.QueueConfig {
	cfg := promconfig.DefaultQueueConfig

	cfg.MaxBackoff = model.Duration(2 * time.Second)
	// Note: we are passing bytes in a MaxSamplesPerSend field.
	cfg.MaxSamplesPerSend = c.OpenTelemetry.MaxBytesPerRequest
	cfg.MinShards = c.OpenTelemetry.MinShards
	cfg.MaxShards = c.OpenTelemetry.MaxShards

	// Note: This is size of a single queue owned by the queue manager.
	cfg.Capacity = c.OpenTelemetry.QueueSize

	return cfg
}

type FileReadFunc func(filename string) ([]byte, error)

func DefaultMainConfig() MainConfig {
	return MainConfig{
		Prometheus: PromConfig{
			WAL:                       DefaultWALDirectory,
			Endpoint:                  DefaultPrometheusEndpoint,
			MaxPointAge:               DurationConfig{DefaultMaxPointAge},
			HealthCheckRequestTimeout: DurationConfig{DefaultHealthCheckTimeout},
		},
		OpenTelemetry: OTelConfig{
			MaxBytesPerRequest: DefaultMaxBytesPerRequest,
			MinShards:          DefaultMinShards,
			MaxShards:          DefaultMaxShards,
			QueueSize:          DefaultQueueSize,
		},
		Admin: AdminConfig{
			Port:                      DefaultAdminPort,
			ListenIP:                  DefaultAdminListenIP,
			HealthCheckPeriod:         DurationConfig{DefaultHealthCheckPeriod},
			HealthCheckThresholdRatio: DefaultHealthCheckThresholdRatio,
		},
		Destination: OTLPConfig{
			Headers:     map[string]string{},
			Attributes:  map[string]string{},
			Timeout:     DurationConfig{DefaultExportTimeout},
			Compression: snappy.Name,
		},
		Diagnostics: OTLPConfig{
			Headers:     map[string]string{},
			Attributes:  map[string]string{},
			Timeout:     DurationConfig{DefaultExportTimeout},
			Compression: snappy.Name,
		},
		LogConfig: LogConfig{
			Level:   "info",
			Format:  "logfmt",
			Verbose: 0,
		},
		StartupTimeout: DurationConfig{
			DefaultStartupTimeout,
		},
	}
}

// Configure is a separate unit of code for testing purposes.
func Configure(args []string, readFunc FileReadFunc) (MainConfig, map[string]string, []*MetadataEntry, error) {
	cfg := DefaultMainConfig()

	a := kingpin.New(filepath.Base(args[0]), briefDescription)

	a.Version(version.Print("opentelemetry-prometheus-sidecar"))

	a.HelpFlag.Short('h')

	// Below we avoid using the kingpin.v2 `Default()` mechanism
	// so that file config overrides default config and flag
	// config overrides file config.

	a.Flag("config-file", "A configuration file.").
		StringVar(&cfg.ConfigFilename)

	makeOTLPFlags := func(lowerPrefix string, op *OTLPConfig) {
		upperPrefix := strings.Title(lowerPrefix)
		a.Flag(lowerPrefix+".endpoint", upperPrefix+" address of a OpenTelemetry Metrics protocol gRPC endpoint (e.g., https://host:port).  Use \"http\" (not \"https\") for an insecure connection.").
			StringVar(&op.Endpoint)

		a.Flag(lowerPrefix+".attribute", upperPrefix+" resource attributes attached to OTLP data (e.g., MyResource=Value1). May be repeated.").
			StringMapVar(&op.Attributes)

		a.Flag(lowerPrefix+".header", upperPrefix+" headers used for OTLP requests (e.g., MyHeader=Value1). May be repeated.").
			StringMapVar(&op.Headers)

		a.Flag(lowerPrefix+".timeout", upperPrefix+" timeout used for OTLP Export() requests").
			DurationVar(&op.Timeout.Duration)

		a.Flag(lowerPrefix+".compression", upperPrefix+" compression used for OTLP requests (e.g., snappy, gzip, none).").
			StringVar(&op.Compression)
	}

	makeOTLPFlags("destination", &cfg.Destination)
	makeOTLPFlags("diagnostics", &cfg.Diagnostics)

	a.Flag("prometheus.wal", "Directory from where to read the Prometheus TSDB WAL. Default: "+DefaultWALDirectory).
		StringVar(&cfg.Prometheus.WAL)

	a.Flag("prometheus.endpoint", "Endpoint where Prometheus hosts its  UI, API, and serves its own metrics. Default: "+DefaultPrometheusEndpoint).
		StringVar(&cfg.Prometheus.Endpoint)

	a.Flag("prometheus.max-point-age", "Skip points older than this, to assist recovery. Default: "+DefaultMaxPointAge.String()).
		DurationVar(&cfg.Prometheus.MaxPointAge.Duration)

	a.Flag("prometheus.health-check-request-timeout", "Timeout used for health-check requests to prometheus. Default: "+DefaultHealthCheckTimeout.String()).
		DurationVar(&cfg.Prometheus.HealthCheckRequestTimeout.Duration)

	var ignoredScrapeIntervals []string
	a.Flag("prometheus.scrape-interval", "Ignored. This is inferred from the Prometheus via api/v1/status/config").
		StringsVar(&ignoredScrapeIntervals)

	a.Flag("admin.port", "Administrative port this process listens on. Default: "+fmt.Sprint(DefaultAdminPort)).
		IntVar(&cfg.Admin.Port)
	a.Flag("admin.listen-ip", "Administrative IP address this process listens on. Default: "+DefaultAdminListenIP).
		StringVar(&cfg.Admin.ListenIP)

	a.Flag("security.root-certificate", "Root CA certificate to use for TLS connections, in PEM format (e.g., root.crt). May be repeated.").
		StringsVar(&cfg.Security.RootCertificates)

	a.Flag("opentelemetry.max-bytes-per-request", fmt.Sprintf("Send at most this many bytes per request. Default: %d", DefaultMaxBytesPerRequest)).
		IntVar(&cfg.OpenTelemetry.MaxBytesPerRequest)

	a.Flag("opentelemetry.min-shards", fmt.Sprintf("Min number of shards, i.e. amount of concurrency. Default: %d", DefaultMinShards)).
		IntVar(&cfg.OpenTelemetry.MinShards)

	a.Flag("opentelemetry.max-shards", fmt.Sprintf("Max number of shards, i.e. amount of concurrency. Default: %d", DefaultMaxShards)).
		IntVar(&cfg.OpenTelemetry.MaxShards)

	a.Flag("opentelemetry.metrics-prefix", "Customized prefix for exporter metrics. If not set, none will be used").
		StringVar(&cfg.OpenTelemetry.MetricsPrefix)

	a.Flag("opentelemetry.queue-size", fmt.Sprintf("Number of points that can accumulate before blocking the reader. Default: %d", DefaultQueueSize)).
		IntVar(&cfg.OpenTelemetry.QueueSize)

	a.Flag("filter", "PromQL metric and attribute matcher which must pass for a series to be forwarded to OpenTelemetry. If repeated, the series must pass any of the filter sets to be forwarded.").
		StringsVar(&cfg.Filters)

	a.Flag("startup.timeout", "Timeout at startup to allow the endpoint to become available. Default: "+DefaultStartupTimeout.String()).
		DurationVar(&cfg.StartupTimeout.Duration)

	a.Flag("healthcheck.period", "Period for internal health checking; set at a minimum to the shortest Promethues scrape period").
		DurationVar(&cfg.Admin.HealthCheckPeriod.Duration)
	a.Flag("healthcheck.threshold-ratio", "Threshold ratio for internal health checking. Default: 0.5").
		Float64Var(&cfg.Admin.HealthCheckThresholdRatio)

	a.Flag(promlogflag.LevelFlagName, promlogflag.LevelFlagHelp).StringVar(&cfg.LogConfig.Level)
	a.Flag(promlogflag.FormatFlagName, promlogflag.FormatFlagHelp).StringVar(&cfg.LogConfig.Format)
	a.Flag("log.verbose", "Verbose logging level: 0 = off, 1 = some, 2 = more; 1 is automatically added when log.level is 'debug'; impacts logging from the gRPC library in particular").
		IntVar(&cfg.LogConfig.Verbose)

	a.Flag("leader-election.enabled", "Enable leader election to choose a single writer.").
		BoolVar(&cfg.LeaderElection.Enabled)

	a.Flag("leader-election.k8s-namespace", "Namespace used for the leadership election lease.").
		StringVar(&cfg.LeaderElection.K8S.Namespace)

	a.Flag("disable-supervisor", "Disable the supervisor.").
		BoolVar(&cfg.DisableSupervisor)
	a.Flag("disable-diagnostics", "Disable diagnostics by default; if unset, diagnostics will be auto-configured to the primary destination").
		BoolVar(&cfg.DisableDiagnostics)

	_, err := a.Parse(args[1:])
	if err != nil {
		return MainConfig{}, nil, nil,
			errors.Wrap(err, "error parsing command-line arguments")
	}

	var (
		metricRenames  map[string]string
		staticMetadata []*MetadataEntry
	)

	if cfg.ConfigFilename != "" {
		data, err := readFunc(cfg.ConfigFilename)
		if err != nil {
			return MainConfig{}, nil, nil,
				errors.Wrap(err, "reading file")
		}

		cfg = DefaultMainConfig()
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

	if err := sanitizeValues("destination attribute", false, cfg.Destination.Attributes); err != nil {
		return MainConfig{}, nil, nil, err
	}
	if err := sanitizeValues("destination header", true, cfg.Destination.Headers); err != nil {
		return MainConfig{}, nil, nil, err
	}

	if cfg.Diagnostics.Endpoint != "" {
		if err := sanitizeValues("diagnostics attribute", false, cfg.Diagnostics.Attributes); err != nil {
			return MainConfig{}, nil, nil, err
		}
		if err := sanitizeValues("diagnostics header", true, cfg.Diagnostics.Headers); err != nil {
			return MainConfig{}, nil, nil, err
		}
	}

	if cfg.OpenTelemetry.MinShards > cfg.OpenTelemetry.MaxShards {
		return MainConfig{}, nil, nil, errors.New("min-shards cannot be greater than max-shards")
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
		{"admin.endpoint", fmt.Sprint("http://", cfg.Admin.ListenIP, ":", cfg.Admin.Port), false},
	} {
		if pair.allowEmpty && pair.value == "" {
			continue
		}
		if pair.value == "" {
			return MainConfig{}, nil, nil, fmt.Errorf("endpoint must be set: %s", pair.name)
		}
		url, err := url.Parse(pair.value)
		if err != nil {
			return MainConfig{}, nil, nil, fmt.Errorf("invalid endpoint: %s: %s: %w", pair.name, pair.value, err)
		}

		switch url.Scheme {
		case "http", "https":
			// Good!
		default:
			return MainConfig{}, nil, nil, fmt.Errorf("endpoints must use http or https: %s: %s", pair.name, pair.value)
		}
	}

	return cfg, metricRenames, staticMetadata, nil
}

func sanitize(val string) string {
	if len(val) == 0 {
		return val
	}
	val = strings.TrimSpace(val)
	if strings.Contains(val, "\"") {
		val = strings.ReplaceAll(val, "\"", "")
	}
	if strings.Contains(val, "'") {
		val = strings.ReplaceAll(val, "'", "")
	}
	return val
}

func sanitizeValues(kind string, downcaseKeys bool, values map[string]string) error {
	for origKey, value := range values {
		key := sanitize(origKey)
		if downcaseKeys {
			key = strings.ToLower(key)
		}
		if key == "" {
			return fmt.Errorf("empty %s key", kind)
		}
		if key != origKey {
			delete(values, origKey)
		}

		value = sanitize(value)

		if strings.Contains(value, "\n") {
			return fmt.Errorf("invalid newline in %s value: %s", kind, key)
		}
		values[key] = value
	}

	return nil
}

func parseConfigFile(data []byte, cfg *MainConfig) (map[string]string, []*MetadataEntry, error) {
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, nil, errors.Wrap(err, "invalid YAML")
	}
	return processMainConfig(cfg)
}

func processMainConfig(cfg *MainConfig) (map[string]string, []*MetadataEntry, error) {
	renameMapping := map[string]string{}
	for _, r := range cfg.MetricRenames {
		renameMapping[r.From] = r.To
	}
	staticMetadata := []*MetadataEntry{}
	for _, sm := range cfg.StaticMetadata {
		switch sm.Type {
		case MetricTypeUntyped:
			// Convert "untyped" to the "unknown" type used internally as of Prometheus 2.5.
			sm.Type = textparse.MetricTypeUnknown
		case textparse.MetricTypeCounter, textparse.MetricTypeGauge, textparse.MetricTypeHistogram,
			textparse.MetricTypeSummary, textparse.MetricTypeUnknown:
		default:
			return nil, nil, errors.Errorf("invalid metric type %q", sm.Type)
		}
		var valueType ValueType
		switch sm.ValueType {
		case "double", "":
			valueType = DOUBLE
		case "int64":
			valueType = INT64
		default:
			return nil, nil, errors.Errorf("invalid value type %q", sm.ValueType)
		}
		staticMetadata = append(
			staticMetadata,
			&MetadataEntry{
				Metric:     sm.Metric,
				MetricType: sm.Type,
				ValueType:  valueType,
				Help:       sm.Help,
			},
		)
	}
	return renameMapping, staticMetadata, nil
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

// PromReady is used for prometheus.WaitForReady() in several
// places.  It is not parsed from the config file or command-line, it
// is here to avoid a test package cycle, primarily.
type PromReady struct {
	Logger                         log.Logger
	PromURL                        *url.URL
	StartupDelayEffectiveStartTime time.Time
	HealthCheckRequestTimeout      time.Duration
}

// TODO: The use of Kind and ValueType are Stackdriver terms that
// relate confusingly with OTLP data point types and temporality.
// See https://github.com/open-telemetry/opentelemetry-proto/issues/274#issuecomment-790844633

type (
	Kind      int
	ValueType int
)

const (
	GAUGE      Kind = 1
	CUMULATIVE Kind = 2
	DELTA      Kind = 3

	DOUBLE       ValueType = 1
	INT64        ValueType = 2
	DISTRIBUTION ValueType = 3
	HISTOGRAM    ValueType = 4
)

// DefaultEndpointPath is the default HTTP path on which Prometheus serves
// the target metadata endpoint.
const (
	PrometheusTargetMetadataEndpointPath = "api/v1/targets/metadata"
	PrometheusMetadataEndpointPath       = "api/v1/metadata"
	PrometheusConfigEndpointPath         = "api/v1/status/config"
)

// The old metric type value for textparse.MetricTypeUnknown that is used in
// Prometheus 2.4 and earlier.
const MetricTypeUntyped = "untyped"

// MetadataEntry is the parsed and checked form of StaticMetadataConfig
type MetadataEntry struct {
	Metric     string
	MetricType textparse.MetricType
	ValueType  ValueType
	Help       string
}
