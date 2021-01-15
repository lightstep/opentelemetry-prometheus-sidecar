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
	"github.com/lightstep/opentelemetry-prometheus-sidecar/metadata"
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
		staticMetadata []*metadata.Entry
		errText        string
	}{
		{
			"empty",
			"",
			map[string]string{},
			[]*metadata.Entry{},
			"",
		},
		{
			"smoke", `
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
			[]*metadata.Entry{
				&metadata.Entry{Metric: "int64_counter", MetricType: textparse.MetricTypeCounter, ValueType: metadata.INT64, Help: "help1"},
				&metadata.Entry{Metric: "double_gauge", MetricType: textparse.MetricTypeGauge, ValueType: metadata.DOUBLE, Help: "help2"},
				&metadata.Entry{Metric: "default_gauge", MetricType: textparse.MetricTypeGauge, ValueType: metadata.DOUBLE},
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

			if diff := cmp.Diff(tt.renameMappings, metricRenames); diff != "" {
				t.Errorf("renameMappings mismatch: %v", diff)
			}
			if diff := cmp.Diff(tt.staticMetadata, staticMetadata); diff != "" {
				t.Errorf("staticMetadata mismatch: %v", diff)
			}
			if tt.errText == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errText)
			}
		})
	}
}

func TestConfiguration(t *testing.T) {
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
			config.DefaultMainConfig(),
			"",
		},
		{
			"only_file", `
destination:
  endpoint: http://womp.womp
  attributes:
    a: b
    c: d
  headers:
    e: f
    g: h
  timeout: 14s

prometheus:
  wal: wal-eeee

startup_delay: 1333s
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
				},
				Admin: AdminConfig{
					ListenAddress: config.DefaultAdminListenAddress,
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
						14 * time.Second,
					},
				},
				Diagnostics: OTLPConfig{
					Headers:    map[string]string{},
					Attributes: map[string]string{},
					Timeout: DurationConfig{
						60 * time.Second,
					},
				},
				LogConfig: LogConfig{
					Level:  "info",
					Format: "logfmt",
				},
				StartupDelay: DurationConfig{
					1333 * time.Second,
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

log_config:
  format: json
  level: error
`,
			[]string{
				"--startup.delay=1333s",
				"--startup.timeout=1777s",
				"--destination.attribute", "c=d",
				"--destination.header", "g=h",
				"--prometheus.wal", "wal-eeee",
				"--log.level=warning",
				"--diagnostics.endpoint", "https://look.here",
				`--filter=l1{l2="v3"}`,
				"--filter", `l4{l5="v6"}`,
			},
			MainConfig{
				Prometheus: PromConfig{
					WAL:      "wal-eeee",
					Endpoint: config.DefaultPrometheusEndpoint,
					MaxPointAge: DurationConfig{
						25 * time.Hour,
					},
				},
				Admin: AdminConfig{
					ListenAddress: config.DefaultAdminListenAddress,
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
				},
				LogConfig: LogConfig{
					Level:  "warning",
					Format: "json",
				},
				StartupDelay: DurationConfig{
					1333 * time.Second,
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

diagnostics:
  endpoint: https://diagnose.me
  headers:
    A: B
  attributes:
    C: D
  timeout: 1h40m

prometheus:
  wal: /volume/wal
  endpoint: http://127.0.0.1:19090/
  max_point_age: 72h

startup_delay: 30s
startup_timeout: 33s

log_config:
  level: warn
  format: json

admin:
  listen_address: 0.0.0.0:10000

security:
  root_certificates:
  - /certs/root1.crt
  - /certs/root2.crt

opentelemetry:
  metrics_prefix: prefix.
  use_meta_labels: true

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
					ListenAddress: "0.0.0.0:10000",
				},
				StartupDelay: DurationConfig{
					30 * time.Second,
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
				},
				OpenTelemetry: OTelConfig{
					MetricsPrefix: "prefix.",
					UseMetaLabels: true,
				},
				Destination: OTLPConfig{
					Endpoint: "https://ingest.staging.lightstep.com:443",
					Attributes: map[string]string{
						"service.name": "demo",
					},
					Headers: map[string]string{
						"Lightstep-Access-Token": "aabbccdd...wwxxyyzz",
					},
					Timeout: DurationConfig{
						600 * time.Second,
					},
				},
				Diagnostics: OTLPConfig{
					Endpoint: "https://diagnose.me",
					Headers: map[string]string{
						"A": "B",
					},
					Attributes: map[string]string{
						"C": "D",
					},
					Timeout: DurationConfig{
						6000 * time.Second,
					},
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
			[]string{"--destination.header=key=\nabcdef\n"},
			func() config.MainConfig {
				cfg := config.DefaultMainConfig()
				cfg.Destination.Headers["key"] = "abcdef"
				return cfg
			}(),
			"",
		},
		{
			"trim header double quotes around both key/val",
			"",
			[]string{"--destination.header=\"key=abcdef\""},
			func() config.MainConfig {
				cfg := config.DefaultMainConfig()
				cfg.Destination.Headers["key"] = "abcdef"
				return cfg
			}(),
			"",
		},
		{
			"trim header double quotes around key only",
			"",
			[]string{"--destination.header=\"key\"=abcdef"},
			func() config.MainConfig {
				cfg := config.DefaultMainConfig()
				cfg.Destination.Headers["key"] = "abcdef"
				return cfg
			}(),
			"",
		},
		{
			"trim header double quotes around val only",
			"",
			[]string{"--destination.header=key=\"abcdef\""},
			func() config.MainConfig {
				cfg := config.DefaultMainConfig()
				cfg.Destination.Headers["key"] = "abcdef"
				return cfg
			}(),
			"",
		},
		{
			"trim header single quotes around both key/val",
			"",
			[]string{"--destination.header='key=abcdef'"},
			func() config.MainConfig {
				cfg := config.DefaultMainConfig()
				cfg.Destination.Headers["key"] = "abcdef"
				return cfg
			}(),
			"",
		},
		{
			"trim header single quotes around key only",
			"",
			[]string{"--destination.header='key'=abcdef"},
			func() config.MainConfig {
				cfg := config.DefaultMainConfig()
				cfg.Destination.Headers["key"] = "abcdef"
				return cfg
			}(),
			"",
		},
		{
			"trim header single quotes around val only",
			"",
			[]string{"--destination.header=key='abcdef'"},
			func() config.MainConfig {
				cfg := config.DefaultMainConfig()
				cfg.Destination.Headers["key"] = "abcdef"
				return cfg
			}(),
			"",
		},
		{
			"check header newlines",
			"",
			[]string{"--destination.header=key=abc\ndef"},
			config.DefaultMainConfig(),
			"invalid newline",
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
				if diff := cmp.Diff(tt.MainConfig, cfg); diff != "" {
					t.Errorf("MainConfig mismatch: %v", diff)
				}
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errText)
			}
		})
	}
}
