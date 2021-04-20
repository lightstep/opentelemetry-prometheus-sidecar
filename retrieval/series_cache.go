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
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	sidecar "github.com/lightstep/opentelemetry-prometheus-sidecar"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/common"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry/doevery"
	"github.com/pkg/errors"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/prometheus/tsdb/record"
	"github.com/prometheus/prometheus/tsdb/wal"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// tsDesc has complete, proto-independent data about a metric data
// point.
type tsDesc struct {
	Name      string
	Labels    labels.Labels // Sorted
	Kind      config.Kind
	ValueType config.ValueType
}

type seriesGetter interface {
	// Same interface as the standard map getter.
	get(ctx context.Context, ref uint64) (*seriesCacheEntry, error)

	// Get the reset timestamp and adjusted value for the input sample.
	getResetAdjusted(entry *seriesCacheEntry, timestamp int64, value float64) (reset int64, adjusted float64)
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
	renames       map[string]string

	// lastCheckpoint holds the index of the last checkpoint we garbage collected for.
	// We don't have to redo garbage collection until a higher checkpoint appears.
	lastCheckpoint int
	mtx            sync.Mutex
	// Map from series reference to various cached information about it.
	entries map[uint64]*seriesCacheEntry
	// Map for jobs where "instance" has been relabelled
	jobInstanceMap map[string]string

	currentSeriesObs metric.Int64UpDownSumObserver

	failingReporter common.FailingReporter
}

type seriesCacheEntry struct {

	// desc is non-nil for series with successful metadata an no
	// semantic conflicts.
	desc *tsDesc

	// metadata is what Prometheus knows about this series,
	// including the expected point kind.
	metadata *config.MetadataEntry

	// lset is a non-nil set of labels for series being exported.
	// This is nil for series that did not match the filter
	// expressions.
	lset labels.Labels

	name   string
	suffix string

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
	lastLookup time.Time
}

var (
	seriesCacheLookupCounter = telemetry.NewCounter(
		"sidecar.metadata.lookups",
		"Number of Metric series lookups",
	)

	seriesCacheRefsCollected = telemetry.NewCounter(
		"sidecar.refs.collected",
		"Number of series refs garbage collected",
	)

	seriesRefsNotFoundCounter = sidecar.OTelMeterMust.NewInt64Counter(
		"sidecar.refs.notfound",
		metric.WithDescription("Number of series ref lookups that were not found"),
	)

	errSeriesNotFound        = fmt.Errorf("series ref not found")
	errSeriesMissingMetadata = fmt.Errorf("series ref missing metadata")
)

func (e *seriesCacheEntry) populated() bool {
	return e.desc != nil
}

func (e *seriesCacheEntry) shouldTryLookup() bool {
	// We'll keep trying until populated.
	return !e.populated() && time.Since(e.lastLookup) > config.DefaultSeriesCacheLookupPeriod
}

func newSeriesCache(
	logger log.Logger,
	dir string,
	filters [][]*labels.Matcher,
	renames map[string]string,
	metaget MetadataGetter,
	metricsPrefix string,
	jobInstanceMap map[string]string,
	failingReporter common.FailingReporter,
) *seriesCache {
	if logger == nil {
		logger = log.NewNopLogger()
	}
	sc := &seriesCache{
		logger:         logger,
		dir:            dir,
		filters:        filters,
		metaget:        metaget,
		entries:        map[uint64]*seriesCacheEntry{},
		metricsPrefix:  metricsPrefix,
		renames:        renames,
		jobInstanceMap: jobInstanceMap,

		failingReporter: failingReporter,
	}

	sc.currentSeriesObs = sidecar.OTelMeterMust.NewInt64UpDownSumObserver(
		"sidecar.series.current",
		func(ctx context.Context, result metric.Int64ObserverResult) {
			sc.mtx.Lock()
			defer sc.mtx.Unlock()

			filtered, invalid, live := 0, 0, 0
			for _, ent := range sc.entries {
				if ent.lset == nil {
					filtered++
				} else if ent.populated() {
					live++
				} else {
					invalid++
				}
			}

			status := attribute.Key("status")

			result.Observe(int64(filtered), status.String("filtered"))
			result.Observe(int64(live), status.String("live"))
			result.Observe(int64(invalid), status.String("invalid"))
		},
		metric.WithDescription(
			"The current number of series in the series cache.",
		),
	)

	return sc
}

func (c *seriesCache) run(ctx context.Context) {
	tick := time.NewTicker(config.DefaultSeriesCacheGarbageCollectionPeriod)
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
func (c *seriesCache) garbageCollect() (retErr error) {
	var collected int64
	defer seriesCacheRefsCollected.Add(context.Background(), collected, &retErr)

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
			collected++
		}
	}
	c.lastCheckpoint = cpNum
	return nil
}

func (c *seriesCache) get(ctx context.Context, ref uint64) (*seriesCacheEntry, error) {
	c.mtx.Lock()
	e, ok := c.entries[ref]
	c.mtx.Unlock()

	if !ok {
		// This is a very serious condition, probably
		// indicates some kind of incomplete checkpoint was
		// read.
		//
		// Since these cannot be tracked distinctly from the
		// other conditions below when observed by the caller
		// (presently, because we do not use status codes,
		// only error=true or error=false).
		seriesRefsNotFoundCounter.Add(context.Background(), 1)
		return nil, errSeriesNotFound
	}

	if e.lset == nil {
		return nil, nil
	}

	if e.shouldTryLookup() {
		if err := c.lookup(ctx, ref); err != nil {
			return nil, errors.Wrap(err, e.name)
		}
	}

	if !e.populated() {
		return nil, errors.Wrapf(errSeriesMissingMetadata, e.name)
	}

	return e, nil
}

// getResetAdjusted takes a sample for a referenced series and returns
// its reset timestamp and adjusted value.
func (c *seriesCache) getResetAdjusted(e *seriesCacheEntry, t int64, v float64) (int64, float64) {
	hasReset := e.hasReset
	e.hasReset = true
	if !hasReset {
		e.resetTimestamp = t
		e.resetValue = v
		e.previousValue = v
		// If we just initialized the reset timestamp, record a zero (i.e., reset).
		// The next sample will be considered relative to resetValue.
		return t, 0
	}
	if v < e.previousValue {
		// If the value has dropped, there's been a reset.
		e.resetValue = 0
		e.resetTimestamp = t
	}
	e.previousValue = v
	return e.resetTimestamp, v - e.resetValue
}

// set the label set for the given reference.
// maxSegment indicates the the highest segment at which the series was possibly defined.
// lset cannot be empty, it must contain at least __name__, job, and instance labels.
func (c *seriesCache) set(ctx context.Context, ref uint64, lset labels.Labels, maxSegment int) error {
	exported := c.filters == nil || matchFilters(lset, c.filters)

	name := lset.Get("__name__")

	if !exported {
		// We can forget these labels forever, don't care b/c
		// they didn't match.  We'll keep this in our entries
		// map so that we can distinguish dropped points from
		// filtered points.
		lset = nil
		c.failingReporter.Set("filtered", name)
	}

	c.mtx.Lock()
	c.entries[ref] = &seriesCacheEntry{
		maxSegment: maxSegment,
		lset:       lset,
		name:       name,
	}
	c.mtx.Unlock()
	return c.lookup(ctx, ref)
}

func (c *seriesCache) jobInstanceKey(job string) string {
	if val, ok := c.jobInstanceMap[job]; ok {
		return val
	}
	return "instance"
}

func (c *seriesCache) lookup(ctx context.Context, ref uint64) (retErr error) {
	now := time.Now()
	c.mtx.Lock()
	entry := c.entries[ref]
	entry.lastLookup = now
	c.mtx.Unlock()

	if entry.lset == nil {
		// in which case the entry did not match the filters.  it was
		// added to failingReporter in set().
		return nil
	}

	failedReason := "unknown"

	defer func() {
		seriesCacheLookupCounter.Add(ctx, 1, &retErr)
		if retErr == nil {
			return
		}
		c.failingReporter.Set(failedReason, entry.name)
	}()

	entryLabels := copyLabels(entry.lset)

	// Remove __name__ label.
	for i, l := range entryLabels {
		if l.Name == "__name__" {
			entryLabels = append(entryLabels[:i], entryLabels[i+1:]...)
			break
		}
	}

	var (
		baseMetricName string
		suffix         string
		job            = entry.lset.Get("job")
		instance       = entry.lset.Get(c.jobInstanceKey(job))
	)
	meta, err := c.metaget.Get(ctx, job, instance, entry.name)
	if err != nil {
		failedReason = "metadata_error"
		return err
	}

	if meta == nil {
		// The full name didn't turn anything up. Check again in case it's a summary,
		// histogram, or counter without the metric name suffix.
		var ok bool
		if baseMetricName, suffix, ok = stripComplexMetricSuffix(entry.name); ok {
			meta, err = c.metaget.Get(ctx, job, instance, baseMetricName)
			if err != nil {
				failedReason = "metadata_error"
				return err
			}
		}
		if meta == nil {
			doevery.TimePeriod(config.DefaultNoisyLogPeriod, func() {
				level.Warn(c.logger).Log(
					"msg", "metadata not found",
					"metric_name", entry.name,
					"labels", fmt.Sprint(entry.lset),
				)
			})
			failedReason = "metadata_missing"
			return errSeriesMissingMetadata
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
		Name:   c.getMetricName(c.metricsPrefix, entry.name),
		Labels: entryLabels,
	}
	sort.Sort(&ts.Labels)

	switch meta.MetricType {
	case textparse.MetricTypeCounter:
		ts.Kind = config.CUMULATIVE
		ts.ValueType = config.DOUBLE
		if meta.ValueType != 0 {
			ts.ValueType = meta.ValueType
		}
		if baseMetricName != "" && suffix == metricSuffixTotal {
			ts.Name = c.getMetricName(c.metricsPrefix, baseMetricName)
		}
	case textparse.MetricTypeGauge, textparse.MetricTypeUnknown:
		ts.Kind = config.GAUGE
		ts.ValueType = config.DOUBLE
		if meta.ValueType != 0 {
			ts.ValueType = meta.ValueType
		}
	case textparse.MetricTypeSummary:
		switch suffix {
		case metricSuffixSum:
			ts.Kind = config.CUMULATIVE
			ts.ValueType = config.DOUBLE
		case metricSuffixCount:
			ts.Kind = config.CUMULATIVE
			ts.ValueType = config.INT64
		case "": // Actual quantiles.
			ts.Kind = config.GAUGE
			ts.ValueType = config.DOUBLE
		default:
			// Note: this branch has been seen for a
			// _bucket suffix, indicating a mix of
			// histogram and summary conventions.
			failedReason = "invalid_suffix"
			return errors.Errorf("unexpected summary suffix %q", suffix)
		}
	case textparse.MetricTypeHistogram:
		// Note: It's unclear why this branch does not check for allowed
		// suffixes the way the Summary branch does. Should it?
		ts.Name = c.getMetricName(c.metricsPrefix, baseMetricName)
		ts.Kind = config.CUMULATIVE
		ts.ValueType = config.DISTRIBUTION
	default:
		failedReason = "unknown_type"
		return errors.Errorf("unexpected metric type %s", meta.MetricType)
	}

	entry.desc = &ts
	entry.metadata = meta
	entry.suffix = suffix

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
