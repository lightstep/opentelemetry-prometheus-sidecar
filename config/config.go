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
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ghodss/yaml"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/pkg/errors"
	promlogflag "github.com/prometheus/common/promlog/flag"
	"github.com/prometheus/common/version"
	"gopkg.in/alecthomas/kingpin.v2"
)

const (
	DefaultStartupDelay       = time.Minute
	DefaultStartupTimeout     = 5 * time.Minute
	DefaultWALDirectory       = "data/wal"
	DefaultAdminListenAddress = "0.0.0.0:9091"
	DefaultPrometheusEndpoint = "http://127.0.0.1:9090/"
	DefaultMaxPointAge        = time.Hour * 25

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

// PointKind is the kind of OTLP data point, with recognized values:
//
// 1. "monotonic-delta-sum" a.k.a. "delta"
// 2. "monotonic-cumulative-sum" a.k.a. "cumulative"
// 3. "non-monotonic-delta-sum" a.k.a. "delta-gauge"
// 4. "non-monotonic-cumulative-sum" a.k.a. "cumulative-gauge"
// 5. "gauge"
// 6. histogram (implied to be cumulative)
// 7. summary
//
// Cases 1-5 address scalar point conversions.
//
// Note that cases (1), (3), and (4) allow translation from Prometheus
// into OTLP's richer data for sum data points.
//
// Cases (2) and (5) map into Prometheus-default behaviors, therefore
// can be used correct the wrong choice of Prometheus instrument, for
// example.
//
// Cases (6) and (7) can be used to declare the expected point kind,
// but not to change structured points into scalar points or scalar
// points into structured points.
type PointKind string

// NumberType is integer or floating point.
type NumberType string

const (
	DoubleType NumberType = "double"
	IntType    NumberType = "int"

	DeltaKind                  PointKind = "delta"
	CumulativeKind             PointKind = "cumulative"
	NonMonotonicDeltaKind      PointKind = "non-monotonic-delta"
	NonMonotonicCumulativeKind PointKind = "non-monotonic-cumulative"
	GaugeKind                  PointKind = "gauge"
	HistogramKind              PointKind = "histogram"
	SummaryKind                PointKind = "summary"
)

type MetadataConfig struct {
	// Name is the metric's exact name.  This field is exclusive
	// with Regexp.
	Name string `json:"name"`

	// Regexp is a regexp matching a metric's name.  This field is
	// exclusive with Name.
	Regexp string `json:"regexp"`

	// PointKind is the point kind.
	PointKind PointKind `json:"point_kind"`

	// NumberType indicates the correct numeric type to use with
	// recognized values "integer", "int64", "double", and
	// "float64".
	NumberType NumberType `json:"number_type"`

	// Description is attached as the metric description.
	Description string `json:"description"`

	// Unit as attached as the unit string.
	Unit string `json:"unit"`
}

type SecurityConfig struct {
	RootCertificates []string `json:"root_certificates"`
}

type DurationConfig struct {
	time.Duration `json:"duration" yaml:"-,inline"`
}

type OTLPConfig struct {
	Endpoint   string            `json:"endpoint"`
	Headers    map[string]string `json:"headers"`
	Attributes map[string]string `json:"attributes"`
	Timeout    DurationConfig    `json:"timeout"`
}

type LogConfig struct {
	Level   string `json:"level"`
	Format  string `json:"format"`
	Verbose int    `json:"verbose"`
}

type PromConfig struct {
	Endpoint    string         `json:"endpoint"`
	WAL         string         `json:"wal"`
	MaxPointAge DurationConfig `json:"max_point_age"`
}

type OTelConfig struct {
	MetricsPrefix string `json:"metrics_prefix"`
	UseMetaLabels bool   `json:"use_meta_labels"`
}

type AdminConfig struct {
	ListenAddress string `json:"listen_address"`
}

type MainConfig struct {
	// Note: These fields are ordered so that JSON and YAML
	// marshal in order of importance.

	Destination    OTLPConfig            `json:"destination"`
	Prometheus     PromConfig            `json:"prometheus"`
	OpenTelemetry  OTelConfig            `json:"opentelemetry"`
	Admin          AdminConfig           `json:"admin"`
	Security       SecurityConfig        `json:"security"`
	Diagnostics    OTLPConfig            `json:"diagnostics"`
	StartupDelay   DurationConfig        `json:"startup_delay"`
	StartupTimeout DurationConfig        `json:"startup_timeout"`
	Filters        []string              `json:"filters"`
	MetricRenames  []MetricRenamesConfig `json:"metric_renames"`
	StaticMetadata []MetadataConfig      `json:"static_metadata"`
	LogConfig      LogConfig             `json:"log_config"`

	// This field cannot be parsed inside a configuration file,
	// only can be set by command-line flag.:
	ConfigFilename string `json:"-" yaml:"-"`
}

type FileReadFunc func(filename string) ([]byte, error)

func DefaultMainConfig() MainConfig {
	return MainConfig{
		Prometheus: PromConfig{
			WAL:         DefaultWALDirectory,
			Endpoint:    DefaultPrometheusEndpoint,
			MaxPointAge: DurationConfig{DefaultMaxPointAge},
		},
		Admin: AdminConfig{
			ListenAddress: DefaultAdminListenAddress,
		},
		Destination: OTLPConfig{
			Headers:    map[string]string{},
			Attributes: map[string]string{},
			Timeout:    DurationConfig{telemetry.DefaultExportTimeout},
		},
		Diagnostics: OTLPConfig{
			Headers:    map[string]string{},
			Attributes: map[string]string{},
			Timeout:    DurationConfig{telemetry.DefaultExportTimeout},
		},
		LogConfig: LogConfig{
			Level:   "info",
			Format:  "logfmt",
			Verbose: 0,
		},
		StartupDelay: DurationConfig{
			DefaultStartupDelay,
		},
		StartupTimeout: DurationConfig{
			DefaultStartupTimeout,
		},
	}
}

// Configure is a separate unit of code for testing purposes.
func Configure(args []string, readFunc FileReadFunc) (MainConfig, map[string]string, []*MetadataConfig, error) {
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
	}

	makeOTLPFlags("destination", &cfg.Destination)
	makeOTLPFlags("diagnostics", &cfg.Diagnostics)

	a.Flag("prometheus.wal", "Directory from where to read the Prometheus TSDB WAL. Default: "+DefaultWALDirectory).
		StringVar(&cfg.Prometheus.WAL)

	a.Flag("prometheus.endpoint", "Endpoint where Prometheus hosts its  UI, API, and serves its own metrics. Default: "+DefaultPrometheusEndpoint).
		StringVar(&cfg.Prometheus.Endpoint)

	a.Flag("prometheus.max-point-age", "Skip points older than this, to assist recovery. Default: "+DefaultMaxPointAge.String()).
		DurationVar(&cfg.Prometheus.MaxPointAge.Duration)

	a.Flag("admin.listen-address", "Administrative HTTP address this process listens on. Default: "+DefaultAdminListenAddress).
		StringVar(&cfg.Admin.ListenAddress)

	a.Flag("security.root-certificate", "Root CA certificate to use for TLS connections, in PEM format (e.g., root.crt). May be repeated.").
		StringsVar(&cfg.Security.RootCertificates)

	a.Flag("opentelemetry.metrics-prefix", "Customized prefix for exporter metrics. If not set, none will be used").
		StringVar(&cfg.OpenTelemetry.MetricsPrefix)

	a.Flag("opentelemetry.use-meta-labels", "Prometheus target labels prefixed with __meta_ map into labels.").
		BoolVar(&cfg.OpenTelemetry.UseMetaLabels)

	a.Flag("filter", "PromQL metric and label matcher which must pass for a series to be forwarded to OpenTelemetry. If repeated, the series must pass any of the filter sets to be forwarded.").
		StringsVar(&cfg.Filters)

	a.Flag("startup.delay", "Delay at startup to allow Prometheus its initial scrape. Default: "+DefaultStartupDelay.String()).
		DurationVar(&cfg.StartupDelay.Duration)

	a.Flag("startup.timeout", "Timeout at startup to allow the endpoint to become available. Default: "+DefaultStartupTimeout.String()).
		DurationVar(&cfg.StartupTimeout.Duration)

	a.Flag(promlogflag.LevelFlagName, promlogflag.LevelFlagHelp).StringVar(&cfg.LogConfig.Level)
	a.Flag(promlogflag.FormatFlagName, promlogflag.FormatFlagHelp).StringVar(&cfg.LogConfig.Format)
	a.Flag("log.verbose", "Verbose logging level: 0 = off, 1 = some, 2 = more; 1 is automatically added when log.level is 'debug'; impacts logging from the gRPC library in particular").
		IntVar(&cfg.LogConfig.Verbose)

	_, err := a.Parse(args[1:])
	if err != nil {
		return MainConfig{}, nil, nil,
			errors.Wrap(err, "error parsing command-line arguments")
	}

	var (
		metricRenames  map[string]string
		staticMetadata []*MetadataConfig
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

	if err := sanitizeValues("destination attribute", cfg.Destination.Attributes); err != nil {
		return MainConfig{}, nil, nil, err
	}
	if err := sanitizeValues("destination header", cfg.Destination.Headers); err != nil {
		return MainConfig{}, nil, nil, err
	}

	if cfg.Diagnostics.Endpoint != "" {
		if err := sanitizeValues("diagnostics attribute", cfg.Diagnostics.Attributes); err != nil {
			return MainConfig{}, nil, nil, err
		}
		if err := sanitizeValues("diagnostics header", cfg.Diagnostics.Headers); err != nil {
			return MainConfig{}, nil, nil, err
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

func sanitizeValues(kind string, values map[string]string) error {
	for origKey, value := range values {
		key := sanitize(origKey)
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

func parseConfigFile(data []byte, cfg *MainConfig) (map[string]string, []*MetadataConfig, error) {
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, nil, errors.Wrap(err, "invalid YAML")
	}
	return processMainConfig(cfg)
}

func ParsePointKind(pk string) (PointKind, error) {
	// This must to include the Prometheus metric type names
	// (e.g., "gauge", "counter", "histogram"), but maps to OTLP
	// point kinds.
	switch strings.ToLower(pk) {
	case "gauge", "untyped", "unknown", "": // Default case.
		return GaugeKind, nil
	case "cumulative", "monotonic-cumulative", "counter":
		return CumulativeKind, nil
	case "delta", "monotonic-delta":
		return DeltaKind, nil
	case "non-monotonic-delta":
		return NonMonotonicDeltaKind, nil
	case "non-monotonic-cumulative":
		return NonMonotonicCumulativeKind, nil
	case "histogram", "gaugehistogram":
		// TODO: 'gaugehistogram' should be distinct
		return HistogramKind, nil
	case "summary":
		return SummaryKind, nil
	default:
		// Note: this includes "info" and "stateset". TODO: Fix.
		return GaugeKind, errors.Errorf("invalid point kind %q", pk)
	}
}

func ParseNumberType(nt string) (NumberType, error) {
	switch strings.ToLower(nt) {
	case "float64", "double", "":
		return DoubleType, nil
	case "int64", "integer":
		return IntType, nil
	default:
		return DoubleType, errors.Errorf("invalid number type %q", nt)
	}
}

func processMainConfig(cfg *MainConfig) (map[string]string, []*MetadataConfig, error) {
	renameMapping := map[string]string{}
	for _, r := range cfg.MetricRenames {
		renameMapping[r.From] = r.To
	}
	var staticMetadata []*MetadataConfig
	for _, parsed := range cfg.StaticMetadata {
		sm := parsed

		if (sm.Name == "") == (sm.Regexp == "") {
			return nil, nil, errors.Errorf("provide one of name or regexp fields %q, %q", sm.Name, sm.Regexp)
		}

		if sm.Regexp != "" {
			if _, err := regexp.Compile(sm.Regexp); err != nil {
				return nil, nil, errors.Wrapf(err, "compile regexp %q", sm.Regexp)
			}
		}

		pk, err := ParsePointKind(string(sm.PointKind))
		if err != nil {
			return nil, nil, err
		}
		sm.PointKind = pk

		nt, err := ParseNumberType(string(sm.NumberType))
		if err != nil {
			return nil, nil, err
		}
		sm.NumberType = nt

		staticMetadata = append(staticMetadata, &sm)
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
