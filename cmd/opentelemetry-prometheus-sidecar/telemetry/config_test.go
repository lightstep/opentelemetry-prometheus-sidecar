// Copyright Lightstep Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package telemetry

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/label"
	"go.opentelemetry.io/otel/sdk/resource"
)

const (
	expectedTracingDisabledMessage = "tracing is disabled by configuration: no endpoint set"
	expectedMetricsDisabledMessage = "metrics are disabled by configuration: no endpoint set"
)

type testLogger struct {
	output []string
}

func (logger *testLogger) addOutput(output string) {
	logger.output = append(logger.output, output)
}

func (logger *testLogger) Fatalf(format string, v ...interface{}) {
	logger.addOutput(fmt.Sprintf(format, v...))
}

func (logger *testLogger) Debugf(format string, v ...interface{}) {
	logger.addOutput(fmt.Sprintf(format, v...))
}

func (logger *testLogger) requireContains(t *testing.T, expected string) {
	t.Helper()
	for _, output := range logger.output {
		if strings.Contains(output, expected) {
			return
		}
	}

	t.Errorf("\nString unexpectedly not found: %v\nIn: %v", expected, logger.output)
}

func (logger *testLogger) requireNotContains(t *testing.T, expected string) {
	t.Helper()
	for _, output := range logger.output {
		if strings.Contains(output, expected) {
			t.Errorf("\nString unexpectedly found: %v\nIn: %v", expected, logger.output)
			return
		}
	}
}

func (logger *testLogger) reset() {
	logger.output = nil
}

type testErrorHandler struct {
}

func (t *testErrorHandler) Handle(err error) {
	fmt.Printf("test error handler handled error: %v\n", err)
}

func testEndpointDisabled(t *testing.T, expected string, opts ...Option) {
	logger := &testLogger{}
	lsOtel := ConfigureOpentelemetry(
		append(opts,
			WithLogger(logger),
		)...,
	)
	defer lsOtel.Shutdown()

	logger.requireContains(t, expected)
}

func TestTraceEndpointDisabled(t *testing.T) {
	testEndpointDisabled(
		t,
		expectedTracingDisabledMessage,
		WithSpanExporterEndpoint(""),
	)
}

func TestMetricEndpointDisabled(t *testing.T) {
	testEndpointDisabled(
		t,
		expectedMetricsDisabledMessage,
		WithMetricExporterEndpoint(""),
	)
}

func TestValidConfig(t *testing.T) {
	logger := &testLogger{}
	lsOtel := ConfigureOpentelemetry(
		WithLogger(logger),
		WithErrorHandler(&testErrorHandler{}),
	)
	defer lsOtel.Shutdown()

	logger.requireContains(t, expectedMetricsDisabledMessage)
	logger.reset()

	lsOtel = ConfigureOpentelemetry(
		WithLogger(logger),
		WithMetricExporterEndpoint("localhost:443"),
		WithSpanExporterEndpoint("localhost:443"),
	)
	defer lsOtel.Shutdown()

	if len(logger.output) > 0 {
		t.Errorf("\nExpected: no logs\ngot: %v", logger.output)
	}
}

func TestInvalidEnvironment(t *testing.T) {
	os.Setenv("OTEL_EXPORTER_OTLP_METRIC_INSECURE", "bleargh")

	logger := &testLogger{}
	lsOtel := ConfigureOpentelemetry(
		WithLogger(logger),
	)
	defer lsOtel.Shutdown()

	logger.requireContains(t, "environment error")
	unsetEnvironment()
}

func TestInvalidMetricsPushIntervalEnv(t *testing.T) {
	os.Setenv("OTEL_EXPORTER_OTLP_METRIC_PERIOD", "300million")

	logger := &testLogger{}
	lsOtel := ConfigureOpentelemetry(
		WithLogger(logger),
		WithSpanExporterEndpoint("127.0.0.1:4000"),
		WithMetricExporterEndpoint("127.0.0.1:4000"),
	)
	defer lsOtel.Shutdown()

	logger.requireContains(t, "setup error: invalid metric reporting period")
	unsetEnvironment()
}

func TestInvalidMetricsPushIntervalConfig(t *testing.T) {
	logger := &testLogger{}
	lsOtel := ConfigureOpentelemetry(
		WithLogger(logger),
		WithSpanExporterEndpoint("127.0.0.1:4000"),
		WithMetricExporterEndpoint("127.0.0.1:4000"),
		WithMetricReportingPeriod(-time.Second),
	)
	defer lsOtel.Shutdown()

	logger.requireContains(t, "setup error: invalid metric reporting period")
	unsetEnvironment()
}

func TestDebugEnabled(t *testing.T) {
	logger := &testLogger{}
	lsOtel := ConfigureOpentelemetry(
		WithLogger(logger),
		WithSpanExporterEndpoint("localhost:443"),
		WithLogLevel("debug"),
		WithResourceAttributes(map[string]string{
			"attr1":     "val1",
			"host.name": "host456",
		}),
	)
	defer lsOtel.Shutdown()
	output := strings.Join(logger.output[:], ",")
	assert.Contains(t, output, "debug logging enabled")
	assert.Contains(t, output, "localhost:443")
}

func TestConfigurationOverrides(t *testing.T) {
	setEnvironment()
	logger := &testLogger{}
	handler := &testErrorHandler{}
	config := newConfig(
		WithSpanExporterEndpoint("override-satellite-url"),
		WithSpanExporterInsecure(false),
		WithMetricExporterEndpoint("override-metrics-url"),
		WithMetricExporterInsecure(false),
		WithLogLevel("info"),
		WithLogger(logger),
		WithErrorHandler(handler),
		WithPropagators([]string{"b3"}),
	)

	expected := Config{
		SpanExporterEndpoint:           "override-satellite-url",
		SpanExporterEndpointInsecure:   false,
		MetricExporterEndpoint:         "override-metrics-url",
		MetricExporterEndpointInsecure: false,
		MetricReportingPeriod:          "30s",
		LogLevel:                       "info",
		Propagators:                    []string{"b3"},
		Resource:                       resource.Empty(),
		logger:                         logger,
		errorHandler:                   handler,
	}
	assert.Equal(t, expected, config)
}

type TestCarrier struct {
	values map[string]string
}

func (t TestCarrier) Get(key string) string {
	return t.values[key]
}

func (t TestCarrier) Set(key string, value string) {
	t.values[key] = value
}

func TestConfigurePropagators(t *testing.T) {
	ctx := otel.ContextWithBaggageValues(context.Background(),
		label.String("keyone", "foo1"),
		label.String("keytwo", "bar1"),
	)
	unsetEnvironment()
	logger := &testLogger{}
	lsOtel := ConfigureOpentelemetry(
		WithLogger(logger),
		WithSpanExporterEndpoint("localhost:443"),
	)
	defer lsOtel.Shutdown()
	ctx, finish := global.Tracer("ex.com/basic").Start(ctx, "foo")
	defer finish.End()
	carrier := TestCarrier{values: map[string]string{}}
	prop := global.TextMapPropagator()
	prop.Inject(ctx, carrier)
	assert.Greater(t, len(carrier.Get("x-b3-traceid")), 0)
	assert.Equal(t, "", carrier.Get("otcorrelations"))

	lsOtel = ConfigureOpentelemetry(
		WithLogger(logger),
		WithSpanExporterEndpoint("localhost:443"),
		WithPropagators([]string{"b3", "cc"}),
	)
	defer lsOtel.Shutdown()
	carrier = TestCarrier{values: map[string]string{}}
	prop = global.TextMapPropagator()
	prop.Inject(ctx, carrier)
	assert.Greater(t, len(carrier.Get("x-b3-traceid")), 0)
	assert.Equal(t, "keyone=foo1,keytwo=bar1", carrier.Get("otcorrelations"))

	logger = &testLogger{}
	lsOtel = ConfigureOpentelemetry(
		WithLogger(logger),
		WithSpanExporterEndpoint("localhost:443"),
		WithPropagators([]string{"invalid"}),
	)
	defer lsOtel.Shutdown()

	expected := "invalid configuration: unsupported propagators. Supported options: b3,cc"
	if !strings.Contains(logger.output[0], expected) {
		t.Errorf("\nString not found: %v\nIn: %v", expected, logger.output[0])
	}
}

func setEnvironment() {
	os.Setenv("OTEL_EXPORTER_OTLP_SPAN_ENDPOINT", "satellite-url")
	os.Setenv("OTEL_EXPORTER_OTLP_SPAN_INSECURE", "true")
	os.Setenv("OTEL_EXPORTER_OTLP_METRIC_ENDPOINT", "metrics-url")
	os.Setenv("OTEL_EXPORTER_OTLP_METRIC_INSECURE", "true")
	os.Setenv("OTEL_LOG_LEVEL", "debug")
	os.Setenv("OTEL_PROPAGATORS", "b3,w3c")
}

func unsetEnvironment() {
	vars := []string{
		"OTEL_EXPORTER_OTLP_SPAN_ENDPOINT",
		"OTEL_EXPORTER_OTLP_SPAN_INSECURE",
		"OTEL_EXPORTER_OTLP_METRIC_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRIC_INSECURE",
		"OTEL_LOG_LEVEL",
		"OTEL_PROPAGATORS",
		"OTEL_EXPORTER_OTLP_METRIC_PERIOD",
	}
	for _, envvar := range vars {
		os.Unsetenv(envvar)
	}
}

func TestMain(m *testing.M) {
	unsetEnvironment()
	os.Exit(m.Run())
}
