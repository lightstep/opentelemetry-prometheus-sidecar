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
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/go-logfmt/logfmt"
	"github.com/stretchr/testify/assert"
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

func (logger *testLogger) Log(kvs ...interface{}) error {
	var buf bytes.Buffer
	enc := logfmt.NewEncoder(&buf)
	err := enc.EncodeKeyvals(kvs...)
	if err != nil {
		panic(err)
	}
	logger.addOutput(buf.String())
	return nil
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
		WithExporterEndpoint(""),
	)
}

func TestMetricEndpointDisabled(t *testing.T) {
	testEndpointDisabled(
		t,
		expectedMetricsDisabledMessage,
		WithExporterEndpoint(""),
	)
}

func TestValidConfig1(t *testing.T) {
	logger, _ := filterDebugLogs()

	lsOtel := ConfigureOpentelemetry(
		WithLogger(logger),
	)
	defer lsOtel.Shutdown()

	logger.requireContains(t, expectedMetricsDisabledMessage)
}

func filterDebugLogs() (*testLogger, log.Logger) {
	tl := &testLogger{}
	return tl, level.NewFilter(tl, level.AllowInfo())
}

func TestInvalidMetricsPushIntervalConfig(t *testing.T) {
	logger := &testLogger{}
	lsOtel := ConfigureOpentelemetry(
		WithLogger(logger),
		WithExporterEndpoint("127.0.0.1:4000"),
		WithMetricReportingPeriod(-time.Second),
	)
	defer lsOtel.Shutdown()

	logger.requireContains(t, "invalid metric reporting period")
}

func TestDebugEnabled(t *testing.T) {
	logger, _ := filterDebugLogs()

	lsOtel := ConfigureOpentelemetry(
		WithLogger(logger),
		WithExporterEndpoint("localhost:443"),
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

type TestCarrier struct {
	values map[string]string
}

func (t TestCarrier) Get(key string) string {
	return t.values[key]
}

func (t TestCarrier) Set(key string, value string) {
	t.values[key] = value
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
