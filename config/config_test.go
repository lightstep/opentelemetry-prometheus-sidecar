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

package config_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/snappy"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/stretchr/testify/require"
)

type (
	MainConfig           = config.MainConfig
	PromConfig           = config.PromConfig
	AdminConfig          = config.AdminConfig
	OTLPConfig           = config.OTLPConfig
	LogConfig            = config.LogConfig
	SecurityConfig       = config.SecurityConfig
	DurationConfig       = config.DurationConfig
	OTelConfig           = config.OTelConfig
	MetricRenamesConfig  = config.MetricRenamesConfig
	StaticMetadataConfig = config.StaticMetadataConfig
)

func TestProcessFileConfig(t *testing.T) {
	for _, tt := range []struct {
		name           string
		yaml           string
		renameMappings map[string]string
		staticMetadata []*config.MetadataEntry
		errText        string
	}{
		{
			"empty",
			"",
			nil,
			nil,
			"endpoint must be set: destination.endpoint",
		},
		{
			"smoke", `
destination:
  endpoint: http://otlp
metric_renames:
- from: from
  to:   to
static_metadata:
- metric:     int64_counter
  type:       counter
  value_type: int64
  help:       help1
- metric:     double_gauge
  type:       gauge
  value_type: double
  help:       help2
- metric:     default_gauge
  type:       gauge
  value_type: double
`,
			map[string]string{"from": "to"},
			[]*config.MetadataEntry{
				&config.MetadataEntry{Metric: "int64_counter", MetricType: textparse.MetricTypeCounter, ValueType: config.INT64, Help: "help1"},
				&config.MetadataEntry{Metric: "double_gauge", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE, Help: "help2"},
				&config.MetadataEntry{Metric: "default_gauge", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE},
			},
			"",
		},
		{
			"missing_metric_type", `
static_metadata:
- metric:     int64_default
  value_type: int64
`,
			nil, nil,
			"invalid metric type \"\"",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const cfgFile = "testFileDotYaml"
			readFunc := func(fn string) ([]byte, error) {
				require.Equal(t, fn, cfgFile)
				return []byte(tt.yaml), nil
			}
			_, metricRenames, staticMetadata, err := config.Configure([]string{
				"program",
				"--config-file=" + cfgFile,
			}, readFunc)

			if tt.errText == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errText)
			}
			if diff := cmp.Diff(tt.renameMappings, metricRenames); diff != "" {
				t.Errorf("renameMappings mismatch: %v", diff)
			}
			if diff := cmp.Diff(tt.staticMetadata, staticMetadata); diff != "" {
				t.Errorf("staticMetadata mismatch: %v", diff)
			}
		})
	}
}

func TestConfiguration(t *testing.T) {
	// Add minimum settings to ensure tests will pass.
	testConfig := func() config.MainConfig {
		cfg := config.DefaultMainConfig()
		cfg.Destination.Endpoint = "http://otlp"
		return cfg
	}
	withFlags := func(fs ...string) []string {
		return append([]string{
			"--destination.endpoint=http://otlp",
		}, fs...)
	}

	for _, tt := range []struct {
		name       string
		yaml       string
		args       []string
		MainConfig MainConfig
		errText    string
	}{
		{
			"empty",
			"",
			nil,
			config.MainConfig{},
			"endpoint must be set: destination.endpoint",
		},
		{
			"only_file", `
destination:
  endpoint: http://womp.womp
  attributes:
    a: b
    C: d
  headers:
    e: f
    G: h
  timeout: 14s
  compression: compression_fmt

prometheus:
  wal: wal-eeee
  scrape_intervals: [22m13s]

startup_timeout: 1777s
`,
			nil,
			MainConfig{
				Prometheus: PromConfig{
					WAL:      "wal-eeee",
					Endpoint: config.DefaultPrometheusEndpoint,
					MaxPointAge: DurationConfig{
						25 * time.Hour,
					},
					MaxTimeseriesPerRequest: 500,
					MinShards:		 1,
					MaxShards:               200,
				},
				Admin: AdminConfig{
					ListenIP:                  config.DefaultAdminListenIP,
					Port:                      config.DefaultAdminPort,
					HealthCheckPeriod:         DurationConfig{config.DefaultHealthCheckPeriod},
					HealthCheckThresholdRatio: config.DefaultHealthCheckThresholdRatio,
				},
				Destination: OTLPConfig{
					Endpoint: "http://womp.womp",
					Attributes: map[string]string{
						"a": "b",
						"C": "d",
					},
					Headers: map[string]string{
						"e": "f",
						"g": "h",
					},
					Timeout: DurationConfig{
						14 * time.Second,
					},
					Compression: "compression_fmt",
				},
				Diagnostics: OTLPConfig{
					Headers:    map[string]string{},
					Attributes: map[string]string{},
					Timeout: DurationConfig{
						60 * time.Second,
					},
					Compression: snappy.Name,
				},
				LogConfig: LogConfig{
					Level:  "info",
					Format: "logfmt",
				},
				StartupTimeout: DurationConfig{
					1777 * time.Second,
				},
			},
			"",
		},
		{
			"invalid_yaml", `
:
  x: y
`,
			nil,
			MainConfig{},
			"invalid YAML",
		},
		{
			"empty_resource_key", ``,
			[]string{"--destination.attribute==value"},
			MainConfig{},
			"empty destination attribute key",
		},
		{
			"empty_header_key", ``,
			[]string{"--destination.header==value"},
			MainConfig{},
			"empty destination header key",
		},
		{
			// Note that attributes and headers are merged, while
			// for other fields flags overwrite file-config.
			"file_and_flag", `
destination:
  endpoint: http://womp.womp
  attributes:
    a: b
  headers:
    e: f

filters:
- one{two="three"}
- four{five="six"}

prometheus:
  wal: bad-guy

log:
  format: json
  level: error
`,
			[]string{
				"--startup.timeout=1777s",
				"--destination.attribute", "c=d",
				"--destination.header", "g=h",
				"--destination.compression", "compression_fmt",
				"--prometheus.wal", "wal-eeee",
				"--prometheus.max-point-age", "10h",
				"--prometheus.max-timeseries-per-request", "5",
				"--prometheus.min-shards", "5",
				"--prometheus.max-shards", "10",
				"--log.level=warning",
				"--healthcheck.period=17s",
				"--healthcheck.threshold-ratio=0.2",
				"--diagnostics.endpoint", "https://look.here",
				"--disable-diagnostics",
				`--filter=l1{l2="v3"}`,
				"--filter", `l4{l5="v6"}`,
			},
			MainConfig{
				Prometheus: PromConfig{
					WAL:      "wal-eeee",
					Endpoint: config.DefaultPrometheusEndpoint,
					MaxPointAge: DurationConfig{
						10 * time.Hour,
					},
					MaxTimeseriesPerRequest: 5,
					MinShards:               5,
					MaxShards:               10,
				},
				Admin: AdminConfig{
					ListenIP:                  config.DefaultAdminListenIP,
					Port:                      config.DefaultAdminPort,
					HealthCheckPeriod:         DurationConfig{17 * time.Second},
					HealthCheckThresholdRatio: 0.2,
				},
				Destination: OTLPConfig{
					Endpoint: "http://womp.womp",
					Attributes: map[string]string{
						"a": "b",
						"c": "d",
					},
					Headers: map[string]string{
						"e": "f",
						"g": "h",
					},
					Timeout: DurationConfig{
						60 * time.Second,
					},
					Compression: "compression_fmt",
				},
				Filters: []string{
					`one{two="three"}`,
					`four{five="six"}`,
					`l1{l2="v3"}`,
					`l4{l5="v6"}`,
				},
				Diagnostics: OTLPConfig{
					Endpoint:   "https://look.here",
					Headers:    map[string]string{},
					Attributes: map[string]string{},
					Timeout: DurationConfig{
						60 * time.Second,
					},
					Compression: snappy.Name,
				},
				DisableDiagnostics: true,
				LogConfig: LogConfig{
					Level:  "warning",
					Format: "json",
				},
				StartupTimeout: DurationConfig{
					1777 * time.Second,
				},
			},
			"",
		},
		{
			"all_settings", `
# Comments work!
destination:
  endpoint: https://ingest.staging.lightstep.com:443
  headers:
    Lightstep-Access-Token: aabbccdd...wwxxyyzz

  attributes:
    service.name: demo
  timeout: 10m
  compression: compression_fmt

diagnostics:
  endpoint: https://diagnose.me
  headers:
    A: B
  attributes:
    C: D
  timeout: 1h40m
  compression: compression_fmt

prometheus:
  wal: /volume/wal
  endpoint: http://127.0.0.1:19090/
  max_point_age: 72h
  max_timeseries_per_request: 10
  min_shards: 10
  max_shards: 20
  scrape_intervals: [30s]

startup_timeout: 33s

log:
  level: warn
  format: json

admin:
  listen_ip: 0.0.0.0
  port: 9999
  health_check_period: 10s
  health_check_threshold_ratio: 0.1

security:
  root_certificates:
  - /certs/root1.crt
  - /certs/root2.crt

opentelemetry:
  metrics_prefix: prefix.

filters:
- metric{label=value}
- other{l1=v1,l2=v2}

metric_renames:
- from: old_metric
  to:   new_metric
- from: mistake
  to:   correct

static_metadata:
- metric:     network_bps
  type:       counter
  value_type: int64
  help:       Number of bits transferred by this process.

`,
			nil,
			MainConfig{
				Security: SecurityConfig{
					RootCertificates: []string{
						"/certs/root1.crt",
						"/certs/root2.crt",
					},
				},
				Admin: AdminConfig{
					ListenIP:                  config.DefaultAdminListenIP,
					Port:                      9999,
					HealthCheckPeriod:         DurationConfig{10 * time.Second},
					HealthCheckThresholdRatio: 0.1,
				},
				StartupTimeout: DurationConfig{
					33 * time.Second,
				},
				Prometheus: PromConfig{
					WAL:      "/volume/wal",
					Endpoint: "http://127.0.0.1:19090/",
					MaxPointAge: DurationConfig{
						72 * time.Hour,
					},
					MaxTimeseriesPerRequest: 10,
					MinShards:               10,
					MaxShards:               20,
				},
				OpenTelemetry: OTelConfig{
					MetricsPrefix: "prefix.",
				},
				Destination: OTLPConfig{
					Endpoint: "https://ingest.staging.lightstep.com:443",
					Attributes: map[string]string{
						"service.name": "demo",
					},
					Headers: map[string]string{
						"lightstep-access-token": "aabbccdd...wwxxyyzz",
					},
					Timeout: DurationConfig{
						600 * time.Second,
					},
					Compression: "compression_fmt",
				},
				Diagnostics: OTLPConfig{
					Endpoint: "https://diagnose.me",
					Headers: map[string]string{
						"a": "B",
					},
					Attributes: map[string]string{
						"C": "D",
					},
					Timeout: DurationConfig{
						6000 * time.Second,
					},
					Compression: "compression_fmt",
				},
				LogConfig: LogConfig{
					Level:  "warn",
					Format: "json",
				},
				Filters: []string{
					"metric{label=value}",
					"other{l1=v1,l2=v2}",
				},
				MetricRenames: []MetricRenamesConfig{
					{From: "old_metric", To: "new_metric"},
					{From: "mistake", To: "correct"},
				},
				StaticMetadata: []StaticMetadataConfig{
					{
						Metric:    "network_bps",
						Type:      "counter",
						ValueType: "int64",
						Help:      "Number of bits transferred by this process.",
					},
				},
			},
			"",
		},
		{
			"trim header whitespace",
			"",
			withFlags("--destination.header=key=\nabcdef\n"),
			func() config.MainConfig {
				cfg := testConfig()
				cfg.Destination.Headers["key"] = "abcdef"
				return cfg
			}(),
			"",
		},
		{
			"trim header double quotes around both key/val",
			"",
			withFlags("--destination.header=\"key=abcdef\""),
			func() config.MainConfig {
				cfg := testConfig()
				cfg.Destination.Headers["key"] = "abcdef"
				return cfg
			}(),
			"",
		},
		{
			"trim header double quotes around key only",
			"",
			withFlags("--destination.header=\"key\"=abcdef"),
			func() config.MainConfig {
				cfg := testConfig()
				cfg.Destination.Headers["key"] = "abcdef"
				return cfg
			}(),
			"",
		},
		{
			"trim header double quotes around val only",
			"",
			withFlags("--destination.header=key=\"abcdef\""),
			func() config.MainConfig {
				cfg := testConfig()
				cfg.Destination.Headers["key"] = "abcdef"
				return cfg
			}(),
			"",
		},
		{
			"trim header single quotes around both key/val",
			"",
			withFlags("--destination.header='key=abcdef'"),
			func() config.MainConfig {
				cfg := testConfig()
				cfg.Destination.Headers["key"] = "abcdef"
				return cfg
			}(),
			"",
		},
		{
			"trim header single quotes around key only",
			"",
			withFlags("--destination.header='key'=abcdef"),
			func() config.MainConfig {
				cfg := testConfig()
				cfg.Destination.Headers["key"] = "abcdef"
				return cfg
			}(),
			"",
		},
		{
			"trim header single quotes around val only",
			"",
			withFlags("--destination.header=key='abcdef'"),
			func() config.MainConfig {
				cfg := testConfig()
				cfg.Destination.Headers["key"] = "abcdef"
				return cfg
			}(),
			"",
		},
		{
			"check header newlines",
			"",
			withFlags("--destination.header=key=abc\ndef"),
			config.MainConfig{},
			"invalid newline",
		},
		{
			"ignored flags", ``,
			[]string{
				"--destination.endpoint=http://localhost:9000",

				// ignored
				"--prometheus.scrape-interval", "1333s",
			},
			func() config.MainConfig {
				cfg := config.DefaultMainConfig()
				cfg.Destination.Endpoint = "http://localhost:9000"
				return cfg
			}(),
			"",
		},
		{
			"min-shards greater than max-shards", ``,
			[]string{
				"--prometheus.min-shards=101",
				"--prometheus.max-shards=100",
			},
			config.MainConfig{},
			"min-shards cannot be greater than max-shards",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const cfgFile = "testFileDotYaml"
			readFunc := func(fn string) ([]byte, error) {
				require.Equal(t, fn, cfgFile)
				return []byte(tt.yaml), nil
			}
			args := []string{"program"}
			if len(tt.yaml) != 0 {
				args = append(args, "--config-file="+cfgFile)
			}
			args = append(args, tt.args...)
			cfg, _, _, err := config.Configure(args, readFunc)
			cfg.ConfigFilename = ""

			if tt.errText == "" {
				require.NoError(t, err)
				if diff := cmp.Diff(tt.MainConfig, cfg); diff != "" {
					t.Errorf("MainConfig mismatch: %v", diff)
				}
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errText)
			}
		})
	}
}
