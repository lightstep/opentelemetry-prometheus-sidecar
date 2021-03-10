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
	"sort"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	sidecar "github.com/lightstep/opentelemetry-prometheus-sidecar"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/metadata"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry/doevery"
	"github.com/pkg/errors"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/prometheus/tsdb/record"
	"github.com/prometheus/prometheus/tsdb/wal"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	droppedSeries = sidecar.OTelMeterMust.NewInt64Counter(
		config.DroppedSeriesMetric,
		metric.WithDescription("Number of series that were dropped, not exported"),
	)
	keyReason = attribute.Key("key_reason")

	droppedSeriesMetadataNotFound = droppedSeries.Bind(
		keyReason.String("metadata_not_found"),
	)
)

// tsDesc has complete, proto-independent data about a metric data
// point.
type tsDesc struct {
	Name      string
	Labels    labels.Labels // Sorted
	Resource  labels.Labels // Sorted
	Kind      metadata.Kind
	ValueType metadata.ValueType
}

type seriesGetter interface {
	// Same interface as the standard map getter.
	get(ctx context.Context, ref uint64) (*seriesCacheEntry, bool, error)

	// Get the reset timestamp and adjusted value for the input sample.
	// If false is returned, the sample should be skipped.
	getResetAdjusted(ref uint64, t int64, v float64) (int64, float64, bool)

	// Attempt to set the new most recent time range for the series with given hash.
	// Returns false if it failed, in which case the sample must be discarded.
	updateSampleInterval(hash uint64, start, end int64) bool
}

// seriesCache holds a mapping from series reference to label set.
// It can garbage collect obsolete entries based on the most recent WAL checkpoint.
// Implements seriesGetter.
type seriesCache struct {
	logger        log.Logger
	dir           string
	filters       [][]*labels.Matcher
	metaget       MetadataGetter
	metricsPrefix string
	extraLabels   labels.Labels
	renames       map[string]string

	// lastCheckpoint holds the index of the last checkpoint we garbage collected for.
	// We don't have to redo garbage collection until a higher checkpoint appears.
	lastCheckpoint int
	mtx            sync.Mutex
	// Map from series reference to various cached information about it.
	entries map[uint64]*seriesCacheEntry
	// Map from series hash to most recently written interval.
	intervals map[uint64]sampleInterval
}

type seriesCacheEntry struct {
	desc     *tsDesc
	metadata *metadata.Entry
	lset     labels.Labels
	suffix   string
	hash     uint64

	// Whether the series has been reset/initialized yet. This is false only for
	// the first sample of a new series in the cache, which causes the initial
	// "reset". After that, it is always true.
	hasReset bool

	// The value and timestamp of the latest reset. The timestamp is when it
	// occurred, and the value is what it was reset to. resetValue will initially
	// be the value of the first sample, and then 0 for every subsequent reset.
	resetValue     float64
	resetTimestamp int64

	// Value of the most recent point seen for the time series. If a new value is
	// less than the previous, then the series has reset.
	previousValue float64

	// maxSegment indicates the maximum WAL segment index in which
	// the series was first logged.
	// By providing it as an upper bound, we can safely delete a series entry
	// if the reference no longer appears in a checkpoint with an index at or above
	// this segment index.
	// We don't require a precise number since the caller may not be able to provide
	// it when retrieving records through a buffered reader.
	maxSegment int

	// Last time we attempted to populate meta information about the series.
	lastRefresh time.Time

	// Whether the series needs to be exported.
	exported bool
}

const refreshInterval = 3 * time.Minute

func (e *seriesCacheEntry) populated() bool {
	return e.desc != nil
}

func (e *seriesCacheEntry) shouldRefresh() bool {
	return !e.populated() && time.Since(e.lastRefresh) > refreshInterval
}

func newSeriesCache(
	logger log.Logger,
	dir string,
	filters [][]*labels.Matcher,
	renames map[string]string,
	metaget MetadataGetter,
	metricsPrefix string,
	extraLabels labels.Labels,
) *seriesCache {
	if logger == nil {
		logger = log.NewNopLogger()
	}
	return &seriesCache{
		logger:        logger,
		dir:           dir,
		filters:       filters,
		metaget:       metaget,
		entries:       map[uint64]*seriesCacheEntry{},
		intervals:     map[uint64]sampleInterval{},
		metricsPrefix: metricsPrefix,
		extraLabels:   extraLabels,
		renames:       renames,
	}
}

func (c *seriesCache) run(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := c.garbageCollect(); err != nil {
				level.Error(c.logger).Log("msg", "garbage collection failed", "err", err)
			}
		}
	}
}

// garbageCollect drops obsolete cache entries based on the contents of the most
// recent checkpoint.
func (c *seriesCache) garbageCollect() error {
	cpDir, cpNum, err := wal.LastCheckpoint(c.dir)
	if errors.Cause(err) == record.ErrNotFound {
		return nil // Nothing to do.
	}
	if err != nil {
		return errors.Wrap(err, "find last checkpoint")
	}
	if cpNum <= c.lastCheckpoint {
		return nil
	}
	sr, err := wal.NewSegmentsReader(cpDir)
	if err != nil {
		return errors.Wrap(err, "open segments")
	}
	defer sr.Close()

	// Scan all series records in the checkpoint and build a set of existing
	// references.
	var (
		r      = wal.NewReader(sr)
		exists = map[uint64]struct{}{}
		dec    record.Decoder
		series []record.RefSeries
	)
	for r.Next() {
		rec := r.Record()
		if dec.Type(rec) != record.Series {
			continue
		}
		series, err = dec.Series(rec, series[:0])
		if err != nil {
			return errors.Wrap(err, "decode series")
		}
		for _, s := range series {
			exists[s.Ref] = struct{}{}
		}
	}
	if r.Err() != nil {
		return errors.Wrap(err, "read checkpoint records")
	}

	// We can cleanup series in our cache that were neither in the current checkpoint nor
	// defined in WAL segments after the checkpoint.
	// References are monotonic but may be inserted into the WAL out of order. Thus we
	// consider the highest possible segment a series was created in.
	c.mtx.Lock()
	defer c.mtx.Unlock()

	for ref, entry := range c.entries {
		if _, ok := exists[ref]; !ok && entry.maxSegment <= cpNum {
			delete(c.entries, ref)
		}
	}
	c.lastCheckpoint = cpNum
	return nil
}

func (c *seriesCache) get(ctx context.Context, ref uint64) (*seriesCacheEntry, bool, error) {
	c.mtx.Lock()
	e, ok := c.entries[ref]
	c.mtx.Unlock()

	if !ok {
		return nil, false, nil
	}
	if e.shouldRefresh() {
		if err := c.refresh(ctx, ref); err != nil {
			return nil, false, err
		}
	}
	if !e.populated() {
		return nil, false, nil
	}
	return e, true, nil
}

// updateSampleInterval attempts to set the new most recent time range for the series with given hash.
// Returns false if it failed, in which case the sample must be discarded.
func (c *seriesCache) updateSampleInterval(hash uint64, start, end int64) bool {
	iv, ok := c.intervals[hash]
	if !ok || iv.accepts(start, end) {
		c.intervals[hash] = sampleInterval{start, end}
		return true
	}
	return false
}

type sampleInterval struct {
	start, end int64
}

func (si *sampleInterval) accepts(start, end int64) bool {
	return (start == si.start && end > si.end) || (start > si.start && start >= si.end)
}

// getResetAdjusted takes a sample for a referenced series and returns
// its reset timestamp and adjusted value.
// If the last return argument is false, the sample should be dropped.
func (c *seriesCache) getResetAdjusted(ref uint64, t int64, v float64) (int64, float64, bool) {
	c.mtx.Lock()
	e, ok := c.entries[ref]
	c.mtx.Unlock()
	if !ok {
		return 0, 0, false
	}
	hasReset := e.hasReset
	e.hasReset = true
	if !hasReset {
		e.resetTimestamp = t
		e.resetValue = v
		e.previousValue = v
		// If we just initialized the reset timestamp, this sample should be skipped.
		// We don't know the window over which the current cumulative value was built up over.
		// The next sample for will be considered from this point onwards.
		return 0, 0, false
	}
	if v < e.previousValue {
		// If the value has dropped, there's been a reset.
		// If the series was reset, set the reset timestamp to be one millisecond
		// before the timestamp of the current sample.
		// We don't know the true reset time but this ensures the range is non-zero
		// while unlikely to conflict with any previous sample.
		e.resetValue = 0
		e.resetTimestamp = t - 1
	}
	e.previousValue = v
	return e.resetTimestamp, v - e.resetValue, true
}

// set the label set for the given reference.
// maxSegment indicates the the highest segment at which the series was possibly defined.
func (c *seriesCache) set(ctx context.Context, ref uint64, lset labels.Labels, maxSegment int) error {
	exported := c.filters == nil || matchFilters(lset, c.filters)

	if !exported {
		return nil
	}

	c.mtx.Lock()
	c.entries[ref] = &seriesCacheEntry{
		maxSegment: maxSegment,
		lset:       lset,
		exported:   exported,
	}
	c.mtx.Unlock()
	return c.refresh(ctx, ref)
}

func (c *seriesCache) refresh(ctx context.Context, ref uint64) error {
	c.mtx.Lock()
	entry := c.entries[ref]
	c.mtx.Unlock()

	entry.lastRefresh = time.Now()
	entryLabels := copyLabels(entry.lset)

	// Remove __name__ label.
	for i, l := range entryLabels {
		if l.Name == "__name__" {
			entryLabels = append(entryLabels[:i], entryLabels[i+1:]...)
			break
		}
	}

	var (
		metricName     = entry.lset.Get("__name__")
		baseMetricName string
		suffix         string
		job            = entry.lset.Get("job")
		instance       = entry.lset.Get("instance")
	)
	meta, err := c.metaget.Get(ctx, job, instance, metricName)
	if err != nil {
		return errors.Wrap(err, "get metadata")
	}

	if meta == nil {
		// The full name didn't turn anything up. Check again in case it's a summary,
		// histogram, or counter without the metric name suffix.
		var ok bool
		if baseMetricName, suffix, ok = stripComplexMetricSuffix(metricName); ok {
			meta, err = c.metaget.Get(ctx, job, instance, baseMetricName)
			if err != nil {
				return errors.Wrap(err, "get metadata")
			}
		}
		if meta == nil {
			droppedSeriesMetadataNotFound.Add(ctx, 1)

			doevery.TimePeriod(config.DefaultNoisyLogPeriod, func() {
				level.Warn(c.logger).Log(
					"msg", "metadata not found",
					"metric_name", metricName,
				)
			})
			return nil
		}
	}
	// Handle label modifications for histograms early so we don't build the label map twice.
	// We have to remove the 'le' label which defines the bucket boundary.
	if meta.MetricType == textparse.MetricTypeHistogram {
		for i, l := range entryLabels {
			if l.Name == "le" {
				entryLabels = append(entryLabels[:i], entryLabels[i+1:]...)
				break
			}
		}
	}

	ts := tsDesc{
		Name:     c.getMetricName(c.metricsPrefix, metricName),
		Labels:   entryLabels,
		Resource: c.extraLabels,
	}
	sort.Sort(&ts.Labels)

	switch meta.MetricType {
	case textparse.MetricTypeCounter:
		ts.Kind = metadata.CUMULATIVE
		ts.ValueType = metadata.DOUBLE
		if meta.ValueType != 0 {
			ts.ValueType = meta.ValueType
		}
		if baseMetricName != "" && suffix == metricSuffixTotal {
			ts.Name = c.getMetricName(c.metricsPrefix, baseMetricName)
		}
	case textparse.MetricTypeGauge, textparse.MetricTypeUnknown:
		ts.Kind = metadata.GAUGE
		ts.ValueType = metadata.DOUBLE
		if meta.ValueType != 0 {
			ts.ValueType = meta.ValueType
		}
	case textparse.MetricTypeSummary:
		switch suffix {
		case metricSuffixSum:
			ts.Kind = metadata.CUMULATIVE
			ts.ValueType = metadata.DOUBLE
		case metricSuffixCount:
			ts.Kind = metadata.CUMULATIVE
			ts.ValueType = metadata.INT64
		case "": // Actual quantiles.
			ts.Kind = metadata.GAUGE
			ts.ValueType = metadata.DOUBLE
		default:
			return errors.Errorf("unexpected metric name suffix %q", suffix)
		}
	case textparse.MetricTypeHistogram:
		ts.Name = c.getMetricName(c.metricsPrefix, baseMetricName)
		ts.Kind = metadata.CUMULATIVE
		ts.ValueType = metadata.DISTRIBUTION
	default:
		return errors.Errorf("unexpected metric type %s", meta.MetricType)
	}

	entry.desc = &ts
	entry.metadata = meta
	entry.suffix = suffix
	entry.hash = hashSeries(ts)

	return nil
}

func (c *seriesCache) getMetricName(prefix, name string) string {
	if repl, ok := c.renames[name]; ok {
		name = repl
	}
	return getMetricName(prefix, name)
}

// matchFilters checks whether any of the supplied filters passes.
func matchFilters(lset labels.Labels, filters [][]*labels.Matcher) bool {
	for _, fs := range filters {
		if matchfilter(lset, fs) {
			return true
		}
	}
	return false
}

// matchfilter checks whether labels match a given list of label matchers.
// All matchers need to match for the function to return true.
func matchfilter(lset labels.Labels, filter []*labels.Matcher) bool {
	for _, matcher := range filter {
		if !matcher.Matches(lset.Get(matcher.Name)) {
			return false
		}
	}
	return true
}
