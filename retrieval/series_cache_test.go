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
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/internal/promtest"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/prometheus/tsdb/record"
	"github.com/prometheus/prometheus/tsdb/wal"
	"github.com/stretchr/testify/require"
)

type testFailingReporter map[string]bool

func (tr testFailingReporter) Set(reason, name string) {
	tr[fmt.Sprint(reason, "/", name)] = true
}

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
	testFailing := testFailingReporter{}
	c := newSeriesCache(logger, dir, nil, nil,
		promtest.MetadataMap{"//": &config.MetadataEntry{MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE}},
		"",
		nil,
		testFailing,
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
		entry, err := c.get(ctx, uint64(i))
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
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
			if entry, err := c.get(ctx, uint64(i)); err != errSeriesNotFound {
				t.Fatalf("unexpected cache entry %d: %v", i, entry)
			}
		}
		// We should be able to read them all.
		for i := 3; i <= 7; i++ {
			entry, err := c.get(ctx, uint64(i))
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
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
			if _, err := c.get(ctx, uint64(i)); err != errSeriesNotFound {
				t.Fatalf("unexpected error: %s", err)
			}
			continue
		}
		entry, err := c.get(ctx, uint64(i))
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if !labels.Equal(entry.lset, labels.FromStrings("a", strconv.Itoa(i))) {
			t.Fatalf("unexpected label set for ref %d: %s", i, entry.lset)
		}
	}
	require.EqualValues(t, map[string]bool{}, testFailing)
}

func TestSeriesCache_Lookup(t *testing.T) {
	metadataMap := promtest.MetadataMap{}
	testFailing := testFailingReporter{}

	c := newSeriesCache(telemetry.DefaultLogger(), "", nil, nil, metadataMap, "", nil, testFailing)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Query unset reference.
	const refID = 1
	entry, err := c.get(ctx, refID)
	if err != errSeriesNotFound {
		t.Fatalf("unexpected error: %s", err)
	}
	if entry != nil {
		t.Fatalf("unexpected series entry found: %v", entry)
	}

	// Set a series but the config.
	err = c.set(ctx, refID, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1"), 5)
	require.Error(t, err)
	require.True(t, errors.Is(err, errSeriesMissingMetadata), err)

	// We should still not receive anything.
	entry, err = c.get(ctx, refID)
	require.Error(t, err)
	require.True(t, errors.Is(err, errSeriesMissingMetadata), err)

	if entry != nil {
		t.Fatalf("unexpected series entry found: %v", entry)
	}

	// Populate the getters with data.
	metadataMap["job1/inst1/metric1"] = &config.MetadataEntry{Metric: "metric1", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE}

	// Hack the timestamp of the last update to be sufficiently in the past that a refresh
	// will be triggered.
	c.entries[refID].lastLookup = time.Now().Add(-2 * config.DefaultSeriesCacheLookupPeriod)

	// Now another get should trigger a refresh, which now finds data.
	entry, err = c.get(ctx, refID)
	if entry == nil || err != nil {
		t.Errorf("expected metadata but got none, error: %s", err)
	}

	require.EqualValues(t, map[string]bool{
		"metadata_missing/metric1": true,
	}, testFailing)
}

func TestSeriesCache_LookupMetadataNotFound(t *testing.T) {
	logBuffer := &bytes.Buffer{}
	defer func() {
		if logBuffer.Len() > 0 {
			t.Log(logBuffer.String())
		}
	}()
	logger := log.NewLogfmtLogger(logBuffer)
	metadataMap := promtest.MetadataMap{}
	testFailing := testFailingReporter{}
	c := newSeriesCache(logger, "", nil, nil, metadataMap, "", nil, testFailing)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const refID = 1
	// Set will trigger a refresh.
	err := c.set(ctx, refID, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1"), 5)
	require.Error(t, err)
	require.True(t, errors.Is(err, errSeriesMissingMetadata), err)

	// Get shouldn't find data because of the previous error.
	entry, err := c.get(ctx, refID)
	require.Error(t, err)
	require.True(t, errors.Is(err, errSeriesMissingMetadata), err)

	if entry != nil {
		t.Fatalf("unexpected series entry found: %v", entry)
	}

	require.EqualValues(t, map[string]bool{
		"metadata_missing/metric1": true,
	}, testFailing)
}

func TestSeriesCache_Filter(t *testing.T) {
	// Populate the getters with data.
	metadataMap := promtest.MetadataMap{
		"job1/inst1/metric1": &config.MetadataEntry{Metric: "metric1", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE},
	}
	logBuffer := &bytes.Buffer{}
	defer func() {
		if logBuffer.Len() > 0 {
			t.Log(logBuffer.String())
		}
	}()
	logger := log.NewLogfmtLogger(logBuffer)
	testFailing := testFailingReporter{}
	c := newSeriesCache(logger, "", [][]*labels.Matcher{
		{
			&labels.Matcher{Type: labels.MatchEqual, Name: "a", Value: "a1"},
			&labels.Matcher{Type: labels.MatchEqual, Name: "b", Value: "b1"},
		},
		{&labels.Matcher{Type: labels.MatchEqual, Name: "c", Value: "c1"}},
	}, nil, metadataMap, "", nil, testFailing)

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
		if _, err := c.get(ctx, uint64(idx)); err != nil {
			t.Fatalf("metric not found: %s", err)
		}
	}
	// Test filtered metric.
	err := c.set(ctx, 100, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1", "a", "a1", "b", "b2"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.get(ctx, 100); err != nil {
		t.Fatalf("error retrieving metric: %s", err)
	}

	require.EqualValues(t, map[string]bool{
		"filtered/metric1": true,
	}, testFailing)
}

func TestSeriesCache_Filter_Complex(t *testing.T) {
	// Populate the getters with data.
	metadataMap := promtest.MetadataMap{
		"job1/inst1/github_metric": &config.MetadataEntry{Metric: "github_metric", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE},
		"job1/inst1/slack_metric":  &config.MetadataEntry{Metric: "slack_metric", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE},
	}
	logBuffer := &bytes.Buffer{}
	defer func() {
		if logBuffer.Len() > 0 {
			t.Log(logBuffer.String())
		}
	}()
	mustNewMatcher := func(mt labels.MatchType, k, v string) *labels.Matcher {
		m, err := labels.NewMatcher(mt, k, v)
		require.NoError(t, err)
		return m
	}
	logger := log.NewLogfmtLogger(logBuffer)
	testFailing := testFailingReporter{}
	c := newSeriesCache(logger, "", [][]*labels.Matcher{
		{
			mustNewMatcher(labels.MatchRegexp, "__name__", "github.+"),
			mustNewMatcher(labels.MatchRegexp, "category", "issues.+"),
		},
	}, nil, metadataMap, "", nil, testFailing)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test that all but one of these are dropped.
	lsets := []labels.Labels{
		labels.FromStrings("__name__", "github_metric", "job", "job1", "instance", "inst1", "category", "issues_xyz"),
		labels.FromStrings("__name__", "github_metric", "job", "job1", "instance", "inst1", "category", "pullrequests_xyz"),
		labels.FromStrings("__name__", "slack_metric", "job", "job1", "instance", "inst1", "category", "issues_xyz"),
	}
	// Only the 0th entry passes the filter.
	for idx, lset := range lsets {
		err := c.set(ctx, uint64(idx), lset, 1)
		require.NoError(t, err)

		entry, err := c.get(ctx, uint64(idx))

		if idx == 0 {
			require.True(t, err == nil && entry != nil, "Err %v", err)
		} else {
			require.True(t, err == nil && entry == nil, "Err %v", err)
		}
	}

	require.EqualValues(t, map[string]bool{
		"filtered/slack_metric":  true,
		"filtered/github_metric": true,
	}, testFailing)
}

func TestSeriesCache_RenameMetric(t *testing.T) {
	// Populate the getters with data.
	metadataMap := promtest.MetadataMap{
		"job1/inst1/metric1": &config.MetadataEntry{Metric: "metric1", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE},
		"job1/inst1/metric2": &config.MetadataEntry{Metric: "metric2", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE},
	}
	logBuffer := &bytes.Buffer{}
	defer func() {
		if logBuffer.Len() > 0 {
			t.Log(logBuffer.String())
		}
	}()
	logger := log.NewLogfmtLogger(logBuffer)
	testFailing := testFailingReporter{}
	c := newSeriesCache(logger, "", nil,
		map[string]string{"metric2": "metric3"},
		metadataMap, "", nil, testFailing)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test base case of metric that's not renamed.
	err := c.set(ctx, 1, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1"), 1)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := c.get(ctx, 1)
	if err != nil {
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
	entry, err = c.get(ctx, 2)
	if err != nil {
		t.Fatalf("metric not found: %s", err)
	}
	if want := getMetricName("", "metric3"); entry.desc.Name != want {
		t.Fatalf("want proto metric name %q but got %q", want, entry.desc.Name)
	}

	require.EqualValues(t, map[string]bool{}, testFailing)
}

func TestSeriesCache_Relabel(t *testing.T) {
	// Populate the getters with data.
	metadataMap := promtest.MetadataMap{
		"job1/inst1/metric1": &config.MetadataEntry{Metric: "metric1", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE},
		"job2/inst1/metric2": &config.MetadataEntry{Metric: "metric2", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE},
	}
	logBuffer := &bytes.Buffer{}
	defer func() {
		if logBuffer.Len() > 0 {
			t.Log(logBuffer.String())
		}
	}()
	logger := log.NewLogfmtLogger(logBuffer)
	testFailing := testFailingReporter{}
	c := newSeriesCache(logger, "", nil,
		map[string]string{"metric2": "metric3"},
		metadataMap, "", map[string]string{"job1": "other_instance_label"},
		testFailing)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Test base case of metric that's not renamed.
	err := c.set(ctx, 1, labels.FromStrings("__name__", "metric1", "job", "job1", "other_instance_label", "inst1"), 1)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := c.get(ctx, 1)
	if err != nil {
		t.Fatalf("metric not found: %s", err)
	}
	if !labels.Equal(entry.lset, labels.FromStrings("__name__", "metric1", "job", "job1", "other_instance_label", "inst1")) {
		t.Fatalf("unexpected labels %q", entry.lset)
	}
	if want := getMetricName("", "metric1"); entry.desc.Name != want {
		t.Fatalf("want proto metric name %q but got %q", want, entry.desc.Name)
	}
	err = c.set(ctx, 2, labels.FromStrings("__name__", "metric2", "job", "job2", "instance", "inst1"), 1)
	if err != nil {
		t.Fatal(err)
	}
	entry, err = c.get(ctx, 2)
	if err != nil {
		t.Fatalf("metric not found: %s", err)
	}
	if want := getMetricName("", "metric3"); entry.desc.Name != want {
		t.Fatalf("want proto metric name %q but got %q", want, entry.desc.Name)
	}

	require.EqualValues(t, map[string]bool{}, testFailing)
}

func TestSeriesCache_ResetBehavior(t *testing.T) {
	// Test the fix in
	// https://github.com/Stackdriver/stackdriver-prometheus-sidecar/pull/263
	logBuffer := &bytes.Buffer{}
	defer func() {
		if logBuffer.Len() > 0 {
			t.Log(logBuffer.String())
		}
	}()
	logger := log.NewLogfmtLogger(logBuffer)
	metadataMap := promtest.MetadataMap{
		"job1/inst1/metric1": &config.MetadataEntry{Metric: "metric1", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE},
	}
	testFailing := testFailingReporter{}
	c := newSeriesCache(logger, "", nil, nil, metadataMap, "", nil, testFailing)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const refID = 1

	if err := c.set(ctx, refID, labels.FromStrings("__name__", "metric1", "job", "job1", "instance", "inst1"), 5); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	entry, err := c.get(ctx, refID)
	require.NoError(t, err)

	type kase struct {
		ts         int64
		value      float64
		reset      int64
		cumulative float64
	}

	const pad = 0 // OTLP allows zero-width points

	// Simulate two resets.
	for i, k := range []kase{
		{1, 10, 1, 0},
		{2, 20, 1, 10},
		{3, 30, 1, 20},
		{4, 40, 1, 30},

		{5, 5, 5 - pad, 5},
		{6, 10, 5 - pad, 10},
		{7, 15, 5 - pad, 15},

		{8, 0, 8 - pad, 0},
		{9, 10, 8 - pad, 10},
	} {
		ts, val := c.getResetAdjusted(entry, k.ts, k.value)

		require.Equal(t, k.reset, ts, "%d", i)
		require.Equal(t, k.cumulative, val, "%d", i)
	}

	require.EqualValues(t, map[string]bool{}, testFailing)
}
