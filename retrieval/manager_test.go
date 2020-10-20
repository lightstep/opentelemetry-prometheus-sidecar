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

	metric_pb "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/metrics/v1"
	resource_pb "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/resource/v1"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/internal/otlptest"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/metadata"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/tail"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/targets"
	promlabels "github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/tsdb"
	"github.com/prometheus/tsdb/labels"
	"github.com/prometheus/tsdb/wal"
)

type nopAppender struct {
	lock sync.Mutex
	samples []*metric_pb.ResourceMetrics
}

func (a *nopAppender) Append(hash uint64, s *metric_pb.ResourceMetrics) error {
	a.lock.Lock()
	defer a.lock.Unlock()

	a.samples = append(a.samples, s)
	return nil
}

func (a *nopAppender) getSamples() []*metric_pb.ResourceMetrics {
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
	tailer, err := tail.Tail(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}

	var enc tsdb.RecordEncoder
	// Write single series record that  we use for all sample records.
	err = w.Log(enc.Series([]tsdb.RefSeries{
		{Ref: 1, Labels: labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1")},
	}, nil))
	if err != nil {
		t.Fatal(err)
	}

	// Populate the getters with data.
	targetMap := targetMap{
		"job1/inst1": &targets.Target{
			Labels: promlabels.FromStrings("job", "job1", "instance", "inst1"),
			DiscoveredLabels: promlabels.FromStrings(
				"project_id", "proj1",
				"namespace", "ns1", "location", "loc1",
				"job", "job1", "__address__", "inst1"),
		},
	}
	metadataMap := metadataMap{
		"job1/inst1/metric1": &metadata.Entry{Metric: "metric1", MetricType: textparse.MetricTypeGauge, Help: "help"},
	}

	r := NewPrometheusReader(nil, dir, tailer, nil, nil, targetMap, metadataMap, &nopAppender{}, "")
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
			samples := make([]tsdb.RefSample, 1000)
			samples[0] = tsdb.RefSample{Ref: 1, T: int64(sz) * 1000}

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

	tailer, err = tail.Tail(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}

	recorder := &nopAppender{}
	r = NewPrometheusReader(nil, dir, tailer, nil, nil, targetMap, metadataMap, recorder, "")
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
			kind metadata.Kind,
			monotonic bool,
			point interface{},
		) error {
			nanos := point.(*metric_pb.DoubleDataPoint).TimeUnixNano
			tseconds := time.Unix(0, int64(nanos)).Unix()

			if tseconds <= int64(progressOffset)-progressBufferMargin {
				t.Fatalf("unexpected record %d for offset %d", i, tseconds)
			}
			return nil
		}, s)
	}

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

func TestHashSeries(t *testing.T) {
	a := tsDesc{
		Name:     "mtype1",
		Labels:   promlabels.Labels{{"l3", "l3"}, {"l4", "l4"}},
		Resource: promlabels.Labels{{"l1", "l1"}, {"l2", "l2"}},
	}
	// Hash a many times and ensure the hash doesn't change. This checks that we don't produce different
	// hashes by unordered map iteration.
	hash := hashSeries(a)
	for i := 0; i < 1000; i++ {
		if hashSeries(a) != hash {
			t.Fatalf("hash changed for same series")
		}
	}
	for _, b := range []tsDesc{
		{
			Name:     "mtype2",
			Labels:   promlabels.Labels{{"l3", "l3"}, {"l4", "l4"}},
			Resource: promlabels.Labels{{"l1", "l1"}, {"l2", "l2"}},
		},
		{
			Name:     "mtype1",
			Labels:   promlabels.Labels{{"l3", "l3"}, {"l4", "l4"}},
			Resource: promlabels.Labels{{"l1", "l1"}, {"l2", "l2-"}},
		},
		{
			Name:     "mtype1",
			Labels:   promlabels.Labels{{"l3", "l3-"}, {"l4", "l4"}},
			Resource: promlabels.Labels{{"l1", "l1"}, {"l2", "l2"}},
		},
	} {
		if hashSeries(b) == hash {
			t.Fatalf("hash for different series did not change")
		}
	}

}
