/*
Copyright 2018 Google Inc.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package retrieval

import (
	"context"
	"io/ioutil"
	"os"
	"sync"
	"testing"
	"time"

	sidecar "github.com/lightstep/opentelemetry-prometheus-sidecar"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/internal/otlptest"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/internal/promtest"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/prometheus"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/tail"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/prometheus/common/version"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/prometheus/tsdb/record"
	"github.com/prometheus/prometheus/tsdb/wal"
	"github.com/stretchr/testify/require"
	metric_pb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resource_pb "go.opentelemetry.io/proto/otlp/resource/v1"
)

type nopAppender struct {
	lock    sync.Mutex
	samples []*metric_pb.Metric
}

func (a *nopAppender) Append(_ context.Context, s *metric_pb.Metric) error {
	a.lock.Lock()
	defer a.lock.Unlock()

	a.samples = append(a.samples, s)
	return nil
}

func (a *nopAppender) getSamples() []*metric_pb.Metric {
	a.lock.Lock()
	defer a.lock.Unlock()

	return a.samples
}

func TestReader_Progress(t *testing.T) {
	dir, err := ioutil.TempDir("", "progress")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithCancel(context.Background())

	w, err := wal.New(nil, nil, dir, false)
	if err != nil {
		t.Fatal(err)
	}

	prom := promtest.NewFakePrometheus(promtest.Config{})

	tailer, err := tail.Tail(ctx, telemetry.DefaultLogger(), dir, prometheus.NewMonitor(prom.ReadyConfig()))
	if err != nil {
		t.Fatal(err)
	}

	var enc record.Encoder
	// Write single series record that  we use for all sample records.
	err = w.Log(enc.Series([]record.RefSeries{
		{Ref: 1, Labels: labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1")},
	}, nil))
	if err != nil {
		t.Fatal(err)
	}

	// Populate the getters with data.
	metadataMap := promtest.MetadataMap{
		"job1/inst1/metric1": &config.MetadataEntry{Metric: "metric1", MetricType: textparse.MetricTypeGauge, Help: "help"},
	}

	failingSet := testFailingReporter{}
	r := NewPrometheusReader(nil, dir, tailer, nil, nil, metadataMap, &nopAppender{}, "", 0, nil, failingSet)
	r.progressSaveInterval = 200 * time.Millisecond

	// Populate sample data
	go func() {
		defer cancel()
		writeCtx, _ := context.WithTimeout(ctx, 2*time.Second)

		for {
			select {
			case <-writeCtx.Done():
				return
			default:
			}
			// Create sample batches but only populate the first sample with a valid series.
			// This way we write more data but only record a single signaling sample
			// that encodes the record's offset in its timestamp.
			sz, err := tailer.Size()
			if err != nil {
				t.Error(err)
				break
			}
			samples := make([]record.RefSample, 1000)
			samples[0] = record.RefSample{Ref: 1, T: int64(sz) * 1000}

			// Note: We must update the segment number in order for
			// the Tail reader to make progress.
			//
			// Note: This uses the default segment size, independent of
			// the actual segment size, because that's what the sidecar
			// uses to calculate Size(), so this expression is consistent.
			prom.SetSegment(sz / wal.DefaultSegmentSize)

			if err := w.Log(enc.Samples(samples, nil)); err != nil {
				t.Error(err)
				break
			}
		}
	}()
	// Proess the WAL until the writing goroutine completes.
	r.Run(ctx, 0)

	progressOffset, err := ReadProgressFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	// We should've head enough time to have save a reasonably large offset.
	if progressOffset <= 2*progressBufferMargin {
		t.Fatalf("saved offset too low at %d", progressOffset)
	}
	writeOffset := tailer.Offset()

	// Initializing a new tailer and reader should read samples again but skip those that are
	// below our offset.
	// Due to the buffer margin, we will still read some old records, but not all of them.
	// Thus we don't need to write any new records to verify correctness.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	tailer, err = tail.Tail(ctx, telemetry.DefaultLogger(), dir, prometheus.NewMonitor(prom.ReadyConfig()))
	if err != nil {
		t.Fatal(err)
	}

	recorder := &nopAppender{}

	r = NewPrometheusReader(nil, dir, tailer, nil, nil, metadataMap, recorder, "", 0, nil, failingSet)
	go r.Run(ctx, progressOffset)

	// Wait for reader to process until the end.
	ctx, _ = context.WithTimeout(ctx, 5*time.Second)
	for {
		select {
		case <-ctx.Done():
			t.Fatal("timed out waiting for reader")
		default:
		}
		if tailer.Offset() >= writeOffset {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	samples := recorder.getSamples()
	if len(samples) == 0 {
		t.Fatal("expected records but got none")
	}

	ctx = context.Background()

	for i, s := range samples {
		vs := otlptest.VisitorState{}
		vs.Visit(ctx, func(
			resource *resource_pb.Resource,
			metricName string,
			kind config.Kind,
			monotonic bool,
			point interface{},
		) error {
			nanos := point.(*metric_pb.DoubleDataPoint).TimeUnixNano
			tseconds := time.Unix(0, int64(nanos)).Unix()

			if tseconds <= int64(progressOffset)-progressBufferMargin {
				t.Fatalf("unexpected record %d for offset %d", i, tseconds)
			}
			return nil
		}, resourceMetric(s))
	}

	require.EqualValues(t, map[string]bool{}, failingSet)
}

func resourceMetric(m *metric_pb.Metric) *metric_pb.ResourceMetrics {
	return otlptest.ResourceMetrics(
		otlptest.Resource(),
		otlptest.InstrumentationLibraryMetrics(
			otlptest.InstrumentationLibrary(sidecar.ExportInstrumentationLibrary, version.Version),
			m,
		),
	)
}

func TestReader_ProgressFile(t *testing.T) {
	dir, err := ioutil.TempDir("", "save_progress")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	offset, err := ReadProgressFile(dir)
	if err != nil {
		t.Fatalf("read progress: %s", err)
	}
	if offset != 0 {
		t.Fatalf("expected offset %d but got %d", 0, offset)
	}
	if err := SaveProgressFile(dir, progressBufferMargin+12345); err != nil {
		t.Fatalf("save progress: %s", err)
	}
	offset, err = ReadProgressFile(dir)
	if err != nil {
		t.Fatalf("read progress: %s", err)
	}
	if offset != 12345 {
		t.Fatalf("expected progress offset %d but got %d", 12345, offset)
	}
}
