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
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/prometheus/tsdb/record"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	common_pb "go.opentelemetry.io/proto/otlp/common/v1"
	metric_pb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resource_pb "go.opentelemetry.io/proto/otlp/resource/v1"
)

const (
	otlpCUMULATIVE = metric_pb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE
)

var (
	errHistogramMetadataMissing = errors.New("histogram metadata missing")
	errSummaryMetadataMissing   = errors.New("summary metadata missing")
	errStalenessMarkerSkipped   = errors.New("staleness marker skipped")
)

// SizedMetric encapsulates a small number of points w/ precomputed
// approximate size.
type SizedMetric struct {
	metric *metric_pb.Metric

	// Note: uint32 is safe because Prometheus uses 128MB WAL segments.
	size  uint32
	count uint32
}

func NewSizedMetric(metric *metric_pb.Metric, count, size int) SizedMetric {
	if count <= 0 || count > size {
		panic(fmt.Sprintf("Invalid count(%d)>size(%d)>=0", count, size))
	}
	if count > math.MaxUint32 || size > math.MaxUint32 {
		panic("Invalid overflow")
	}
	return SizedMetric{
		metric: metric,
		count:  uint32(count),
		size:   uint32(size),
	}
}

func (s SizedMetric) Size() int {
	return int(s.size)
}

func (s SizedMetric) Count() int {
	return int(s.count)
}

func (s SizedMetric) Metric() *metric_pb.Metric {
	return s.metric
}

// Appender appends a time series with exactly one data point.
type Appender interface {
	Append(s SizedMetric)
}

type sampleBuilder struct {
	series      seriesGetter
	maxPointAge time.Duration
}

// next extracts the next sample from the TSDB input sample list and returns
// the remainder of the input.
//
// Note in cases when no timeseries point is produced, the return value has a
// nil timeseries and a nil error.  These are observable as the difference between
// "processed" and "produced" in the calling code (see manager.go).  TODO: Add
// a label to identify each of the paths below.
func (b *sampleBuilder) next(ctx context.Context, samples []record.RefSample) (*metric_pb.Metric, []record.RefSample, error) {
	sample := samples[0]
	tailSamples := samples[1:]

	if math.IsNaN(sample.V) {
		// Note: This includes stale markers, which are
		// specific NaN values defined in prometheus/tsdb.
		return nil, tailSamples, errStalenessMarkerSkipped
	}

	entry, err := b.series.get(ctx, sample.Ref)

	if err != nil {
		return nil, tailSamples, errors.Wrap(err, "get series information")
	}

	if entry == nil {
		// The point belongs to a filtered-out series.
		return nil, tailSamples, nil
	}

	// Allocate the proto and fill in its metadata.
	point := protoMetric(entry.desc)
	attrs := protoAttributes(entry.desc.Labels)

	var resetTimestamp int64
	switch entry.metadata.MetricType {
	case textparse.MetricTypeCounter:
		var value float64
		resetTimestamp, value = b.series.getResetAdjusted(entry, sample.T, sample.V)

		if entry.metadata.ValueType == config.INT64 {
			point.Data = &metric_pb.Metric_Sum{
				Sum: monotonicIntegerPoint(attrs, resetTimestamp, sample.T, value),
			}
		} else {
			point.Data = &metric_pb.Metric_Sum{
				Sum: monotonicDoublePoint(attrs, resetTimestamp, sample.T, value),
			}
		}

	case textparse.MetricTypeGauge, textparse.MetricTypeUnknown:
		if entry.metadata.ValueType == config.INT64 {
			point.Data = &metric_pb.Metric_Gauge{
				Gauge: intGauge(attrs, sample.T, sample.V),
			}
		} else {
			point.Data = &metric_pb.Metric_Gauge{
				Gauge: doubleGauge(attrs, sample.T, sample.V),
			}
		}

	case textparse.MetricTypeSummary:
		// We pass in the original lset for matching since Prometheus's target label must
		// be the same as well.
		var value *metric_pb.SummaryDataPoint
		value, resetTimestamp, tailSamples, err = b.buildSummary(ctx, entry.metadata.Metric, entry.lset, samples)
		if value == nil || err != nil {
			if err == nil {
				err = errSummaryMetadataMissing
			}
			return nil, tailSamples, err
		}

		value.Attributes = attrs
		value.StartTimeUnixNano = getNanos(resetTimestamp)
		value.TimeUnixNano = getNanos(sample.T)

		doubleSummary := &metric_pb.Summary{
			DataPoints: []*metric_pb.SummaryDataPoint{
				value,
			},
		}

		point.Data = &metric_pb.Metric_Summary{
			Summary: doubleSummary,
		}

	case textparse.MetricTypeHistogram:
		// We pass in the original lset for matching since Prometheus's target label must
		// be the same as well.
		// Note: Always using Histogram points, ignores entry.metadata.ValueType.
		var value *metric_pb.HistogramDataPoint
		value, resetTimestamp, tailSamples, err = b.buildHistogram(ctx, entry.metadata.Metric, entry.lset, samples)
		if value == nil || err != nil {
			if err == nil {
				err = errHistogramMetadataMissing
			}
			return nil, tailSamples, err
		}

		value.Attributes = attrs
		value.StartTimeUnixNano = getNanos(resetTimestamp)
		value.TimeUnixNano = getNanos(sample.T)

		doubleHist := &metric_pb.Histogram{
			AggregationTemporality: otlpCUMULATIVE,
			DataPoints: []*metric_pb.HistogramDataPoint{
				value,
			},
		}

		point.Data = &metric_pb.Metric_Histogram{
			Histogram: doubleHist,
		}

	default:
		return nil, tailSamples, errors.Errorf("unexpected metric type %s", entry.metadata.MetricType)
	}

	if b.maxPointAge > 0 {
		when := time.Unix(sample.T/1000, int64(time.Duration(sample.T%1000)*time.Millisecond))
		if time.Since(when) > b.maxPointAge {
			// Note: Counts as a skipped point (as if
			// filtered out), not a dropped point.
			return nil, tailSamples, nil
		}
	}
	return point, tailSamples, nil
}

func protoAttribute(l labels.Label) *common_pb.KeyValue {
	return &common_pb.KeyValue{
		Key: l.Name,
		Value: &common_pb.AnyValue{
			Value: &common_pb.AnyValue_StringValue{
				StringValue: l.Value,
			},
		},
	}
}

func protoMetric(desc *tsDesc) *metric_pb.Metric {
	return &metric_pb.Metric{
		Name:        desc.Name,
		Description: "", // TODO
		Unit:        "", // TODO
	}
}

func protoAttributes(labels labels.Labels) []*common_pb.KeyValue {
	ret := make([]*common_pb.KeyValue, len(labels))
	for i := range labels {
		ret[i] = protoAttribute(labels[i])
	}
	return ret
}

func LabelsToResource(labels labels.Labels) *resource_pb.Resource {
	return &resource_pb.Resource{
		Attributes: protoAttributes(labels),
	}
}

const (
	metricSuffixBucket = "_bucket"
	metricSuffixSum    = "_sum"
	metricSuffixCount  = "_count"
	metricSuffixTotal  = "_total"
)

func stripComplexMetricSuffix(name string) (prefix string, suffix string, ok bool) {
	if strings.HasSuffix(name, metricSuffixBucket) {
		return name[:len(name)-len(metricSuffixBucket)], metricSuffixBucket, true
	}
	if strings.HasSuffix(name, metricSuffixCount) {
		return name[:len(name)-len(metricSuffixCount)], metricSuffixCount, true
	}
	if strings.HasSuffix(name, metricSuffixSum) {
		return name[:len(name)-len(metricSuffixSum)], metricSuffixSum, true
	}
	if strings.HasSuffix(name, metricSuffixTotal) {
		return name[:len(name)-len(metricSuffixTotal)], metricSuffixTotal, true
	}
	return name, "", false
}

func getMetricName(prefix string, promName string) string {
	if prefix == "" {
		return promName
	}
	return prefix + promName
}

// getNanos converts a millisecond timestamp into a OTLP nanosecond timestamp.
func getNanos(t int64) uint64 {
	return uint64(time.Duration(t) * time.Millisecond / time.Nanosecond)
}

type distribution struct {
	bounds []float64
	values []uint64
}

func (d *distribution) Len() int {
	return len(d.bounds)
}

func (d *distribution) Less(i, j int) bool {
	return d.bounds[i] < d.bounds[j]
}

func (d *distribution) Swap(i, j int) {
	d.bounds[i], d.bounds[j] = d.bounds[j], d.bounds[i]
	d.values[i], d.values[j] = d.values[j], d.values[i]
}

// buildSummary consumes series from the beginning of the input slice that belong to a summary
// with the given metric name and label set.
// It returns the reset timestamp along with the summary.
// NOTE: buildSummary() EXTENSIVELY mimics the logic of buildHistogram() - consider refactoring (properly).
func (b *sampleBuilder) buildSummary(
	ctx context.Context,
	baseName string,
	matchLset labels.Labels,
	samples []record.RefSample,
) (*metric_pb.SummaryDataPoint, int64, []record.RefSample, error) {
	var (
		consumed       int
		count, sum     float64
		resetTimestamp int64
		lastTimestamp  int64
		values         = make([]*metric_pb.SummaryDataPoint_ValueAtQuantile, 0)
	)
	// We assume that all series belonging to the summary are sequential. Consume series
	// until we hit a new metric.
Loop:
	for i, s := range samples {
		e, err := b.series.get(ctx, s.Ref)
		if err != nil {
			// Note: This case may or may not trigger the
			// len(samples) == len(newSamples) test in
			// manager.go. The important part here is that
			// we may skip or may not skip any points that
			// belong to the histogram, but if we don't
			// the manager safetly advances.
			return nil, 0, samples, err
		}
		if e == nil {
			// These points were filtered. This seems like
			// an impossible situation.
			break
		}
		name := e.lset.Get("__name__")
		// The series matches if it has the same base name, the remainder is a valid summary suffix,
		// and the labels aside from the `quantile` and __name__ label match up.
		if !strings.HasPrefix(name, baseName) || !labelsEqual(e.lset, matchLset, "quantile") {
			break
		}
		// This is an edge case of having an adjacent point with the same base name and labels,
		// but different type.
		if e.metadata.MetricType != textparse.MetricTypeSummary {
			break
		}
		// In general, a scrape cannot contain the same (set of) series repeatedlty but for different timestamps.
		// It could still happen with bad clients though and we are doing it in tests for simplicity.
		// If we detect the same series as before but for a different timestamp, return the summary up to this
		// series and leave the duplicate time series untouched on the input.
		if i > 0 && s.T != lastTimestamp {
			// TODO: counter
			break
		}
		lastTimestamp = s.T

		rt, v := b.series.getResetAdjusted(e, s.T, s.V)

		switch e.suffix {
		case metricSuffixSum:
			sum = v
		case metricSuffixCount:
			count = v
			// We take the count series as the authoritative source for the overall reset timestamp.
			resetTimestamp = rt
		case "": // Actual values at quantiles.
			quantile, err := strconv.ParseFloat(e.lset.Get("quantile"), 64)

			if err != nil {
				consumed++
				// TODO: increment metric.
				continue
			}
			values = append(values, &metric_pb.SummaryDataPoint_ValueAtQuantile{
				Quantile: quantile,
				Value:    v,
			})
		default:
			break Loop
		}
		consumed++
	}
	// Don't emit a sample if no reset timestamp was set because the
	// count series was missing.
	if resetTimestamp == 0 {
		// TODO add a counter for this event.
		if consumed == 0 {
			// This may be caused by a change of metadata or metadata conflict.
			// There was no "quantile" label, or there was no _sum or _count suffix.
			return nil, 0, samples[1:], errSummaryMetadataMissing
		}
		return nil, 0, samples[consumed:], nil
	}

	summary := &metric_pb.SummaryDataPoint{
		Count:          uint64(count),
		Sum:            sum,
		QuantileValues: values,
	}
	return summary, resetTimestamp, samples[consumed:], nil
}

// buildHistogram consumes series from the beginning of the input slice that belong to a histogram
// with the given metric name and label set.
// It returns the reset timestamp along with the distrubution.
func (b *sampleBuilder) buildHistogram(
	ctx context.Context,
	baseName string,
	matchLset labels.Labels,
	samples []record.RefSample,
) (*metric_pb.HistogramDataPoint, int64, []record.RefSample, error) {
	var (
		consumed       int
		count, sum     float64
		resetTimestamp int64
		lastTimestamp  int64
		dist           = distribution{bounds: make([]float64, 0, 20), values: make([]uint64, 0, 20)}
	)
	// We assume that all series belonging to the histogram are sequential. Consume series
	// until we hit a new metric.
Loop:
	for i, s := range samples {
		e, err := b.series.get(ctx, s.Ref)
		if err != nil {
			// Note: This case may or may not trigger the
			// len(samples) == len(newSamples) test in
			// manager.go. The important part here is that
			// we may skip or may not skip any points that
			// belong to the histogram, but if we don't
			// the manager safetly advances.
			return nil, 0, samples, err
		}
		if e == nil {
			// These points were filtered. This seems like
			// an impossible situation.
			break
		}
		name := e.lset.Get("__name__")
		// The series matches if it has the same base name, the remainder is a valid histogram suffix,
		// and the labels aside from the le and __name__ label match up.
		if !strings.HasPrefix(name, baseName) || !labelsEqual(e.lset, matchLset, "le") {
			break
		}
		// In general, a scrape cannot contain the same (set of) series repeatedlty but for different timestamps.
		// It could still happen with bad clients though and we are doing it in tests for simplicity.
		// If we detect the same series as before but for a different timestamp, return the histogram up to this
		// series and leave the duplicate time series untouched on the input.
		if i > 0 && s.T != lastTimestamp {
			// TODO: counter
			break
		}
		lastTimestamp = s.T

		rt, v := b.series.getResetAdjusted(e, s.T, s.V)

		switch name[len(baseName):] {
		case metricSuffixSum:
			sum = v
		case metricSuffixCount:
			count = v
			// We take the count series as the authoritative source for the overall reset timestamp.
			resetTimestamp = rt
		case metricSuffixBucket:
			upper, err := strconv.ParseFloat(e.lset.Get("le"), 64)

			if err != nil {
				consumed++
				// TODO: increment metric.
				continue
			}
			dist.bounds = append(dist.bounds, upper)
			dist.values = append(dist.values, uint64(v))
		default:
			break Loop
		}
		consumed++
	}
	// Don't emit a sample if no reset timestamp was set because the
	// count series was missing.
	if resetTimestamp == 0 {
		// TODO add a counter for this event. Note there is
		// more validation we could do: the sum should agree
		// with the buckets.
		if consumed == 0 {
			// This may be caused by a change of metadata or metadata conflict.
			// There was no "le" label, or there was no _sum or _count suffix.
			return nil, 0, samples[1:], errHistogramMetadataMissing
		}
		return nil, 0, samples[consumed:], nil
	}
	// We do not assume that the buckets in the sample batch are in order, so we sort them again here.
	// The code below relies on this to convert between Prometheus's and the output's bucketing approaches.
	sort.Sort(&dist)
	// Reuse slices we already populated to build final bounds and values.
	var (
		values  = dist.values[:0]
		bounds  = dist.bounds[:0]
		prevVal uint64
	)
	// Note: dist.bounds and dist.values have the same size.
	for i := range dist.bounds {
		val := dist.values[i] - prevVal
		prevVal = dist.values[i]
		values = append(values, val)
	}

	if len(dist.bounds) > 0 {
		bounds = dist.bounds[:len(dist.bounds)-1]
	}
	histogram := &metric_pb.HistogramDataPoint{
		Count:          uint64(count),
		Sum:            sum,
		BucketCounts:   values,
		ExplicitBounds: bounds,
	}
	return histogram, resetTimestamp, samples[consumed:], nil
}

// labelsEqual checks whether two label sets for a series are equal aside from a specified
// common label and __name__.
func labelsEqual(a, b labels.Labels, commonLabel string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i].Name == commonLabel || a[i].Name == "__name__" {
			i++
			continue
		}
		if b[j].Name == commonLabel || b[j].Name == "__name__" {
			j++
			continue
		}
		if a[i] != b[j] {
			return false
		}
		i++
		j++
	}
	// Consume trailing common and __name__ labels so the check below passes correctly.
	for i < len(a) {
		if a[i].Name == commonLabel || a[i].Name == "__name__" {
			i++
			continue
		}
		break
	}
	for j < len(b) {
		if b[j].Name == commonLabel || b[j].Name == "__name__" {
			j++
			continue
		}
		break
	}
	// If one label set still has labels left, they are not equal.
	return i == len(a) && j == len(b)
}

func monotonicIntegerPoint(attrs []*common_pb.KeyValue, start, end int64, value float64) *metric_pb.Sum {
	integer := &metric_pb.NumberDataPoint{
		Attributes:        attrs,
		StartTimeUnixNano: getNanos(start),
		TimeUnixNano:      getNanos(end),
		Value:             &metric_pb.NumberDataPoint_AsInt{AsInt: int64(value + 0.5)},
	}
	return &metric_pb.Sum{
		IsMonotonic:            true,
		AggregationTemporality: otlpCUMULATIVE,
		DataPoints:             []*metric_pb.NumberDataPoint{integer},
	}
}

func monotonicDoublePoint(attrs []*common_pb.KeyValue, start, end int64, value float64) *metric_pb.Sum {
	double := &metric_pb.NumberDataPoint{
		Attributes:        attrs,
		StartTimeUnixNano: getNanos(start),
		TimeUnixNano:      getNanos(end),
		Value:             &metric_pb.NumberDataPoint_AsDouble{AsDouble: value},
	}
	return &metric_pb.Sum{
		IsMonotonic:            true,
		AggregationTemporality: otlpCUMULATIVE,
		DataPoints:             []*metric_pb.NumberDataPoint{double},
	}
}

func intGauge(attrs []*common_pb.KeyValue, ts int64, value float64) *metric_pb.Gauge {
	integer := &metric_pb.NumberDataPoint{
		Attributes:   attrs,
		TimeUnixNano: getNanos(ts),
		Value:        &metric_pb.NumberDataPoint_AsInt{AsInt: int64(value + 0.5)},
	}
	return &metric_pb.Gauge{
		DataPoints: []*metric_pb.NumberDataPoint{integer},
	}
}

func doubleGauge(attrs []*common_pb.KeyValue, ts int64, value float64) *metric_pb.Gauge {
	double := &metric_pb.NumberDataPoint{
		Attributes:   attrs,
		TimeUnixNano: getNanos(ts),
		Value:        &metric_pb.NumberDataPoint_AsDouble{AsDouble: value},
	}
	return &metric_pb.Gauge{
		DataPoints: []*metric_pb.NumberDataPoint{double},
	}
}
