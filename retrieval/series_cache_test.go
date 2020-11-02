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
	"bytes"
	"context"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/metadata"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/targets"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/prometheus/tsdb/record"
	"github.com/prometheus/prometheus/tsdb/wal"
)

// This test primarily verifies the garbage collection logic of the cache.
// The getters are verified integrated into the sample builder in transform_test.go
func TestScrapeCache_GarbageCollect(t *testing.T) {
	dir, err := ioutil.TempDir("", "scrape_cache")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Initialize the series cache with "empty" target and metadata maps.
	// The series we use below have no job, instance, and __name__ labels set, which
	// will result in those empty lookup keys.
	logBuffer := &bytes.Buffer{}
	defer func() {
		if logBuffer.Len() > 0 {
			t.Log(logBuffer.String())
		}
	}()
	logger := log.NewLogfmtLogger(logBuffer)
	c := newSeriesCache(logger, dir, nil, nil,
		targetMap{"/": &targets.Target{}},
		metadataMap{"//": &metadata.Entry{MetricType: textparse.MetricTypeGauge, ValueType: metadata.DOUBLE}},
		"",
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Add the series to the cache. Normally, they would have been inserted during
	// tailing - either with the checkpoint index or a segment index at or below.
	c.set(ctx, 1, labels.FromStrings("a", "1"), 0)
	c.set(ctx, 2, labels.FromStrings("a", "2"), 5)
	c.set(ctx, 3, labels.FromStrings("a", "3"), 9)
	c.set(ctx, 4, labels.FromStrings("a", "4"), 10)
	c.set(ctx, 5, labels.FromStrings("a", "5"), 11)
	c.set(ctx, 6, labels.FromStrings("a", "6"), 12)
	c.set(ctx, 7, labels.FromStrings("a", "7"), 13)

	// We should be able to read them all.
	for i := 1; i <= 7; i++ {
		entry, ok, err := c.get(ctx, uint64(i))
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if !ok {
			t.Fatalf("entry with ref %d not found", i)
		}
		if !labels.Equal(entry.lset, labels.FromStrings("a", strconv.Itoa(i))) {
			t.Fatalf("unexpected label set for ref %d: %s", i, entry.lset)
		}
	}

	// Create a checkpoint with index 10, in which some old series were dropped.
	cp10dir := filepath.Join(dir, "checkpoint.00010")
	{
		series := []record.RefSeries{
			{Ref: 3, Labels: labels.FromStrings("a", "3")},
			{Ref: 4, Labels: labels.FromStrings("a", "4")},
		}
		w, err := wal.New(nil, nil, cp10dir, false)
		if err != nil {
			t.Fatal(err)
		}
		var enc record.Encoder
		if err := w.Log(enc.Series(series, nil)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}

	// Run garbage collection. Afterwards the first two series should be gone.
	// Put in a loop to verify that garbage collection is idempotent.
	for i := 0; i < 10; i++ {
		if err := c.garbageCollect(); err != nil {
			t.Fatal(err)
		}
		for i := 1; i < 2; i++ {
			if entry, ok, err := c.get(ctx, uint64(i)); err != nil {
				t.Fatalf("unexpected error: %s", err)
			} else if ok {
				t.Fatalf("unexpected cache entry %d: %s", i, entry.lset)
			}
		}
		// We should be able to read them all.
		for i := 3; i <= 7; i++ {
			entry, ok, err := c.get(ctx, uint64(i))
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if !ok {
				t.Fatalf("label set with ref %d not found", i)
			}
			if !labels.Equal(entry.lset, labels.FromStrings("a", strconv.Itoa(i))) {
				t.Fatalf("unexpected label set for ref %d: %s", i, entry.lset)
			}
		}
	}

	// We create a higher checkpoint, which garbageCollect should detect on its next call.
	cp12dir := filepath.Join(dir, "checkpoint.00012")
	{
		series := []record.RefSeries{
			{Ref: 4, Labels: labels.FromStrings("a", "4")},
		}
		w, err := wal.New(nil, nil, cp12dir, false)
		if err != nil {
			t.Fatal(err)
		}
		var enc record.Encoder
		if err := w.Log(enc.Series(series, nil)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(cp10dir); err != nil {
		t.Fatal(err)
	}
	if err := c.garbageCollect(); err != nil {
		t.Fatal(err)
	}
	//  Only series 4 and 7 should be left.
	for i := 1; i <= 7; i++ {
		if i != 4 && i != 7 {
			if entry, ok, err := c.get(ctx, uint64(i)); err != nil {
				t.Fatalf("unexpected error: %s", err)
			} else if ok {
				t.Fatalf("unexpected cache entry %d: %s", i, entry.lset)
			}
			continue
		}
		entry, ok, err := c.get(ctx, uint64(i))
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if !ok {
			t.Fatalf("entry with ref %d not found", i)
		}
		if !labels.Equal(entry.lset, labels.FromStrings("a", strconv.Itoa(i))) {
			t.Fatalf("unexpected label set for ref %d: %s", i, entry.lset)
		}
	}
}

func TestSeriesCache_Refresh(t *testing.T) {
	targetMap := targetMap{}
	metadataMap := metadataMap{}
	logBuffer := &bytes.Buffer{}
	defer func() {
		if logBuffer.Len() > 0 {
			t.Log(logBuffer.String())
		}
	}()
	logger := log.NewLogfmtLogger(logBuffer)
	c := newSeriesCache(logger, "", nil, nil, targetMap, metadataMap, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Query unset reference.
	const refID = 1
	entry, ok, err := c.get(ctx, refID)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if ok || entry != nil {
		t.Fatalf("unexpected series entry found: %v", entry)
	}

	// Set a series but the metadata and target getters won't have sufficient information for it.
	if err := c.set(ctx, refID, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1"), 5); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if !strings.Contains(logBuffer.String(), "target not found") {
		t.Errorf("expected error \"target not found\", got: %v", logBuffer)
	}
	// We should still not receive anything.
	entry, ok, err = c.get(ctx, refID)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if ok || entry != nil {
		t.Fatalf("unexpected series entry found: %v", entry)
	}

	// Populate the getters with data.
	targetMap["job1/inst1"] = &targets.Target{
		Labels:           labels.FromStrings("job", "job1", "instance", "inst1"),
		DiscoveredLabels: labels.FromStrings("__resource_a", "resource2_a"),
	}
	metadataMap["job1/inst1/metric1"] = &metadata.Entry{Metric: "metric1", MetricType: textparse.MetricTypeGauge, ValueType: metadata.DOUBLE}

	// Hack the timestamp of the last update to be sufficiently in the past that a refresh
	// will be triggered.
	c.entries[refID].lastRefresh = time.Now().Add(-2 * refreshInterval)

	// Now another get should trigger a refresh, which now finds data.
	entry, ok, err = c.get(ctx, refID)
	if entry == nil || !ok || err != nil {
		t.Errorf("expected metadata but got none, error: %s", err)
	}
	if entry == nil || !entry.exported {
		t.Errorf("expected to get exported entry")
	}
}

func TestSeriesCache_RefreshMetadataNotFound(t *testing.T) {
	logBuffer := &bytes.Buffer{}
	defer func() {
		if logBuffer.Len() > 0 {
			t.Log(logBuffer.String())
		}
	}()
	logger := log.NewLogfmtLogger(logBuffer)
	targetMap := targetMap{
		"job1/inst1": &targets.Target{
			Labels:           labels.FromStrings("job", "job1", "instance", "inst1"),
			DiscoveredLabels: labels.FromStrings("__resource_a", "resource2_a"),
		},
	}
	metadataMap := metadataMap{}
	c := newSeriesCache(logger, "", nil, nil, targetMap, metadataMap, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const refID = 1
	// Set will trigger a refresh.
	if err := c.set(ctx, refID, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1"), 5); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if !strings.Contains(logBuffer.String(), "metadata not found") {
		t.Errorf("expected error \"metadata not found\", got: %v", logBuffer)
	}

	// Get shouldn't find data because of the previous error.
	entry, ok, err := c.get(ctx, refID)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if ok || entry != nil {
		t.Fatalf("unexpected series entry found: %v", entry)
	}
}

func TestSeriesCache_Filter(t *testing.T) {
	// Populate the getters with data.
	targetMap := targetMap{
		"job1/inst1": &targets.Target{
			Labels:           labels.FromStrings("job", "job1", "instance", "inst1"),
			DiscoveredLabels: labels.FromStrings("__resource_a", "resource2_a"),
		},
	}
	metadataMap := metadataMap{
		"job1/inst1/metric1": &metadata.Entry{Metric: "metric1", MetricType: textparse.MetricTypeGauge, ValueType: metadata.DOUBLE},
	}
	logBuffer := &bytes.Buffer{}
	defer func() {
		if logBuffer.Len() > 0 {
			t.Log(logBuffer.String())
		}
	}()
	logger := log.NewLogfmtLogger(logBuffer)
	c := newSeriesCache(logger, "", [][]*labels.Matcher{
		{
			&labels.Matcher{Type: labels.MatchEqual, Name: "a", Value: "a1"},
			&labels.Matcher{Type: labels.MatchEqual, Name: "b", Value: "b1"},
		},
		{&labels.Matcher{Type: labels.MatchEqual, Name: "c", Value: "c1"}},
	}, nil, targetMap, metadataMap, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test that metrics that pass a single filterset do not get dropped.
	lsets := []labels.Labels{
		labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1", "a", "a1", "b", "b1"),
		labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1", "c", "c1"),
	}
	for idx, lset := range lsets {
		err := c.set(ctx, uint64(idx), lset, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, err := c.get(ctx, uint64(idx)); !ok || err != nil {
			t.Fatalf("metric not found: %s", err)
		}
	}
	// Test filtered metric.
	err := c.set(ctx, 100, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1", "a", "a1", "b", "b2"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.get(ctx, 100); err != nil {
		t.Fatalf("error retrieving metric: %s", err)
	} else if ok {
		t.Fatalf("metric was not filtered")
	}
}

func TestSeriesCache_RenameMetric(t *testing.T) {
	// Populate the getters with data.
	targetMap := targetMap{
		"job1/inst1": &targets.Target{
			Labels:           labels.FromStrings("job", "job1", "instance", "inst1"),
			DiscoveredLabels: labels.FromStrings("__resource_a", "resource2_a"),
		},
	}
	metadataMap := metadataMap{
		"job1/inst1/metric1": &metadata.Entry{Metric: "metric1", MetricType: textparse.MetricTypeGauge, ValueType: metadata.DOUBLE},
		"job1/inst1/metric2": &metadata.Entry{Metric: "metric2", MetricType: textparse.MetricTypeGauge, ValueType: metadata.DOUBLE},
	}
	logBuffer := &bytes.Buffer{}
	defer func() {
		if logBuffer.Len() > 0 {
			t.Log(logBuffer.String())
		}
	}()
	logger := log.NewLogfmtLogger(logBuffer)
	c := newSeriesCache(logger, "", nil,
		map[string]string{"metric2": "metric3"},
		targetMap, metadataMap, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test base case of metric that's not renamed.
	err := c.set(ctx, 1, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1"), 1)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok, err := c.get(ctx, 1)
	if !ok || err != nil {
		t.Fatalf("metric not found: %s", err)
	}
	if !labels.Equal(entry.lset, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1")) {
		t.Fatalf("unexpected labels %q", entry.lset)
	}
	if want := getMetricName("", "metric1"); entry.desc.Name != want {
		t.Fatalf("want proto metric name %q but got %q", want, entry.desc.Name)
	}
	err = c.set(ctx, 2, labels.FromStrings("__name__", "metric2", "job", "job1", "instance", "inst1"), 1)
	if err != nil {
		t.Fatal(err)
	}
	entry, ok, err = c.get(ctx, 2)
	if !ok || err != nil {
		t.Fatalf("metric not found: %s", err)
	}
	if want := getMetricName("", "metric3"); entry.desc.Name != want {
		t.Fatalf("want proto metric name %q but got %q", want, entry.desc.Name)
	}
}
