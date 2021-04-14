// Copyright 2017 The Prometheus Authors
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

package main

import (
	"bytes"
	"context"
	"io/ioutil"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/common"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/internal/otlptest"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/internal/promtest"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/prometheus/tsdb/record"
	"github.com/prometheus/prometheus/tsdb/wal"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	otlpmetrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	otlpresource "go.opentelemetry.io/proto/otlp/resource/v1"
	grpcmeta "google.golang.org/grpc/metadata"
)

func TestValidationErrorReporting(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	// Create a WAL with 3 series, 5 points.  Two of them are
	// counters, so after resets we have 3 series, 5 points.
	dir, err := ioutil.TempDir("", "test_validation")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	w, err := wal.NewSize(nil, nil, dir, 1<<16, false)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var encoder record.Encoder

	ts := time.Now().Unix() * 1000

	require.NoError(t, w.Log(
		encoder.Series([]record.RefSeries{
			{
				Ref: 1,
				Labels: labels.Labels{
					{Name: "job", Value: "job1"},
					{Name: "instance", Value: "inst1"},
					{Name: "__name__", Value: "counter"},
				},
			},
			{
				Ref: 2,
				Labels: labels.Labels{
					{Name: "job", Value: "job1"},
					{Name: "instance", Value: "inst1"},
					{Name: "__name__", Value: "gauge"},
				},
			},
			{
				Ref: 3,
				Labels: labels.Labels{
					{Name: "job", Value: "job1"},
					{Name: "instance", Value: "inst1"},
					{Name: "__name__", Value: "correct"},
				},
			},
		}, nil),
		encoder.Samples([]record.RefSample{
			// Note the names above do not correlate with
			// type--there are two counters according to
			// the metadata returned (see below) and they
			// each have a first cumulative report of 100
			// (with different reset values).
			{Ref: 1, T: ts, V: 100},
			{Ref: 2, T: ts, V: 1000},
			{Ref: 3, T: ts, V: 10000},
			{Ref: 2, T: ts + 1000, V: 1100},
			{Ref: 3, T: ts + 1000, V: 10100},
		}, nil),
	))

	require.NoError(t, w.Close())

	// Create an OTLP server that returns the following gRPC Trailers
	ms := newTestServer(t, grpcmeta.MD{
		"otlp-points-dropped":  {"2"},
		"otlp-metrics-dropped": {"1"},
		"otlp-invalid-reason1": {"count"},
		"otlp-invalid-reason2": {"gauge", "mistake"},
	})
	defer ms.Stop()
	ms.runDiagnosticsService(nil)
	ms.runPrometheusService(promtest.Config{
		// Conflicting types for "counter" and "gauge".
		Metadata: promtest.MetadataMap{
			"job1/inst1/counter": &config.MetadataEntry{
				Metric:     "counter",
				MetricType: textparse.MetricTypeGauge,
			},
			"job1/inst1/gauge": &config.MetadataEntry{
				Metric:     "gauge",
				MetricType: textparse.MetricTypeCounter,
			},
			"job1/inst1/correct": &config.MetadataEntry{
				Metric:     "correct",
				MetricType: textparse.MetricTypeCounter,
			},
		},
	})

	// Start a sidecar to read the WAL and report diagnostics,
	// includ the invalid metrics.
	cmd := exec.Command(
		os.Args[0],
		append(e2eTestMainSupervisorFlags,
			// Note: the next two flags ensure both the
			// destination and diagnostics output go to
			// the same place.
			"--destination.endpoint=http://127.0.0.1:19000",
			"--diagnostics.endpoint=http://127.0.0.1:19000",
			"--prometheus.wal", dir,
			"--startup.timeout=15s",
			"--healthcheck.period=5s",
			"--destination.timeout=5s",
		)...)

	cmd.Env = append(os.Environ(), "RUN_MAIN=1")

	var bout, berr bytes.Buffer
	cmd.Stdout = &bout
	cmd.Stderr = &berr
	if err = cmd.Start(); err != nil {
		t.Errorf("execution error: %v", err)
		return
	}

	invalid := map[string]bool{}
	timer := time.NewTimer(time.Second * 10)
	defer timer.Stop()

	// Wait for 3 specific points, then 2 specific meta points.
	var droppedPointsFound, droppedSeriesFound int64
	var got = 0
outer:
	for got < 5 || droppedPointsFound == 0 || droppedSeriesFound == 0 {
		var data *metrics.ResourceMetrics
		select {
		case data = <-ms.metrics:
		case <-timer.C:
			t.Error("test timeout: ", got, droppedPointsFound, droppedSeriesFound)
			break outer
		}

		var vs otlptest.VisitorState
		vs.Visit(context.Background(), func(
			_ *otlpresource.Resource,
			name string,
			kind config.Kind,
			_ bool,
			point interface{},
		) error {
			switch name {
			case "counter", "gauge", "correct":
				if point.(*otlpmetrics.DoubleDataPoint).Value == 0 {
					// OK!
				} else {
					require.InEpsilon(t, 100, point.(*otlpmetrics.DoubleDataPoint).Value, 0.01)
				}
				got++
			case config.DroppedPointsMetric:
				droppedPointsFound = point.(*otlpmetrics.IntDataPoint).Value
			case config.DroppedSeriesMetric:
				droppedSeriesFound = point.(*otlpmetrics.IntDataPoint).Value
			case config.FailingMetricsMetric:
				labels := point.(*otlpmetrics.IntDataPoint).Labels

				var reason, mname string
				for _, label := range labels {
					switch attribute.Key(label.Key) {
					case common.DroppedKeyReason:
						reason = label.Value
					case common.MetricNameKey:
						mname = label.Value
					}
				}
				invalid[reason+"/"+mname] = true
			}
			return nil
		}, data)
	}

	_ = cmd.Process.Signal(os.Interrupt)
	_ = cmd.Wait()

	t.Logf("stdout: %v\n", bout.String())
	t.Logf("stderr: %v\n", berr.String())

	// We saw the correct metrics.
	require.EqualValues(t, map[string]bool{
		"reason1/count":   true,
		"reason2/gauge":   true,
		"reason2/mistake": true,
	}, invalid)

	// Correct drop summary:
	require.Equal(t, int64(2), droppedPointsFound) // from server response
	require.Equal(t, int64(1), droppedSeriesFound) // from server response

	for _, expect := range []string{
		// We didn't start the trace service but received data.
		`unknown service opentelemetry.proto.collector.trace.v1.TraceService`,
		// We log the two validation errors.
		`reason=reason1 names=[count]`,
		`reason=reason2 names="[gauge mistake]"`,
	} {
		require.Contains(t, berr.String(), expect)
	}
}
