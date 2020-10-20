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
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	sidecar "github.com/lightstep/opentelemetry-prometheus-sidecar"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/metadata"
	"github.com/pkg/errors"
	"github.com/prometheus/common/version"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/tsdb"
	tsdbLabels "github.com/prometheus/tsdb/labels"

	common_pb "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/common/v1"
	metric_pb "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/metrics/v1"
	resource_pb "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/resource/v1"
)

const (
	otlpCUMULATIVE = metric_pb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE
)

// Appender appends a time series with exactly one data point. A hash for the series
// (but not the data point) must be provided.
// The client may cache the computed hash more easily, which is why its part of the call
// and not done by the Appender's implementation.
type Appender interface {
	Append(hash uint64, s *metric_pb.ResourceMetrics) error
}

type sampleBuilder struct {
	series seriesGetter
}

// next extracts the next sample from the TSDB input sample list and returns
// the remainder of the input.
func (b *sampleBuilder) next(ctx context.Context, samples []tsdb.RefSample) (*metric_pb.ResourceMetrics, uint64, []tsdb.RefSample, error) {
	sample := samples[0]
	tailSamples := samples[1:]

	if math.IsNaN(sample.V) {
		// TODO: counter?
		return nil, 0, tailSamples, nil
	}

	entry, ok, err := b.series.get(ctx, sample.Ref)
	if err != nil {
		return nil, 0, samples, errors.Wrap(err, "get series information")
	}
	if !ok {
		// TODO: counter?  simple negative lookup
		return nil, 0, tailSamples, nil
	}

	if !entry.exported {
		// TODO: counter?
		return nil, 0, tailSamples, nil
	}
	// Allocate the proto and fill in its metadata.
	//
	// TODO This code does not try to combine more than one point
	// into a ResourceMetrics, which is possible here. The hope is
	// to use an OTel-Go SDK metrics processor to implement this
	// functionality, where the OTLP exporter already applies
	// this.
	ts, point := protoTimeseries(entry.desc)
	labels := protoStringLabels(entry.desc.Labels)

	var resetTimestamp int64

	switch entry.metadata.MetricType {
	case textparse.MetricTypeCounter:
		var value float64
		resetTimestamp, value, ok = b.series.getResetAdjusted(sample.Ref, sample.T, sample.V)
		if !ok {
			// TODO: counter
			return nil, 0, tailSamples, nil
		}

		if entry.metadata.ValueType == metadata.INT64 {
			point.Data = &metric_pb.Metric_IntSum{
				IntSum: monotonicIntegerPoint(labels, resetTimestamp, sample.T, value),
			}
		} else {
			point.Data = &metric_pb.Metric_DoubleSum{
				DoubleSum: monotonicDoublePoint(labels, resetTimestamp, sample.T, value),
			}
		}

	case textparse.MetricTypeGauge, textparse.MetricTypeUnknown:
		if entry.metadata.ValueType == metadata.INT64 {
			point.Data = &metric_pb.Metric_IntGauge{
				IntGauge: intGauge(labels, sample.T, sample.V),
			}
		} else {
			point.Data = &metric_pb.Metric_DoubleGauge{
				DoubleGauge: doubleGauge(labels, sample.T, sample.V),
			}
		}

	case textparse.MetricTypeSummary:
		switch entry.suffix {
		case metricSuffixSum:
			var value float64
			resetTimestamp, value, ok = b.series.getResetAdjusted(sample.Ref, sample.T, sample.V)
			if !ok {
				return nil, 0, tailSamples, nil
			}
			point.Data = &metric_pb.Metric_DoubleSum{
				DoubleSum: monotonicDoublePoint(labels, resetTimestamp, sample.T, value),
			}
		case metricSuffixCount:
			var value float64
			resetTimestamp, value, ok = b.series.getResetAdjusted(sample.Ref, sample.T, sample.V)
			if !ok {
				return nil, 0, tailSamples, nil
			}
			point.Data = &metric_pb.Metric_IntSum{
				IntSum: monotonicIntegerPoint(labels, resetTimestamp, sample.T, value),
			}
		case "": // Actual quantiles.
			point.Data = &metric_pb.Metric_DoubleGauge{
				DoubleGauge: doubleGauge(labels, sample.T, sample.V),
			}
		default:
			return nil, 0, tailSamples, errors.Errorf("unexpected metric name suffix %q", entry.suffix)
		}

	case textparse.MetricTypeHistogram:
		// We pass in the original lset for matching since Prometheus's target label must
		// be the same as well.
		// Note: Always using DoubleHistogram points, ignores entry.metadata.ValueType.
		var value *metric_pb.DoubleHistogramDataPoint
		value, resetTimestamp, tailSamples, err = b.buildHistogram(ctx, entry.metadata.Metric, entry.lset, samples)
		if value == nil || err != nil {
			return nil, 0, tailSamples, err
		}

		value.Labels = labels
		value.StartTimeUnixNano = getNanos(resetTimestamp)
		value.TimeUnixNano = getNanos(sample.T)

		doubleHist := &metric_pb.DoubleHistogram{
			AggregationTemporality: otlpCUMULATIVE,
			DataPoints: []*metric_pb.DoubleHistogramDataPoint{
				value,
			},
		}

		point.Data = &metric_pb.Metric_DoubleHistogram{
			DoubleHistogram: doubleHist,
		}

	default:
		return nil, 0, samples[1:], errors.Errorf("unexpected metric type %s", entry.metadata.MetricType)
	}

	if !b.series.updateSampleInterval(entry.hash, resetTimestamp, sample.T) {
		return nil, 0, tailSamples, nil
	}
	return ts, entry.hash, tailSamples, nil
}

func protoLabel(l labels.Label) *common_pb.KeyValue {
	return &common_pb.KeyValue{
		Key: l.Name,
		Value: &common_pb.AnyValue{
			Value: &common_pb.AnyValue_StringValue{
				StringValue: l.Value,
			},
		},
	}
}

func protoStringLabel(l labels.Label) *common_pb.StringKeyValue {
	return &common_pb.StringKeyValue{
		Key:   l.Name,
		Value: l.Value,
	}
}

func protoResourceAttributes(labels labels.Labels) []*common_pb.KeyValue {
	ret := make([]*common_pb.KeyValue, len(labels))
	for i := range labels {
		ret[i] = protoLabel(labels[i])
	}
	return ret
}

func protoStringLabels(labels labels.Labels) []*common_pb.StringKeyValue {
	ret := make([]*common_pb.StringKeyValue, len(labels))
	for i := range labels {
		ret[i] = protoStringLabel(labels[i])
	}
	return ret
}

func protoTimeseries(desc *tsDesc) (*metric_pb.ResourceMetrics, *metric_pb.Metric) {
	metric := &metric_pb.Metric{
		Name:        desc.Name,
		Description: "", // TODO
		Unit:        "", // TODO
	}
	return &metric_pb.ResourceMetrics{
		Resource: &resource_pb.Resource{
			Attributes: protoResourceAttributes(desc.Resource),
		},
		InstrumentationLibraryMetrics: []*metric_pb.InstrumentationLibraryMetrics{
			&metric_pb.InstrumentationLibraryMetrics{
				InstrumentationLibrary: &common_pb.InstrumentationLibrary{
					Name:    sidecar.InstrumentationLibrary,
					Version: version.Version,
				},
				Metrics: []*metric_pb.Metric{metric},
			},
		},
	}, metric
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
	return prefix + "/" + promName
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

// buildHistogram consumes series from the beginning of the input slice that belong to a histogram
// with the given metric name and label set.
// It returns the reset timestamp along with the distrubution.
func (b *sampleBuilder) buildHistogram(
	ctx context.Context,
	baseName string,
	matchLset tsdbLabels.Labels,
	samples []tsdb.RefSample,
) (*metric_pb.DoubleHistogramDataPoint, int64, []tsdb.RefSample, error) {
	var (
		consumed       int
		count, sum     float64
		resetTimestamp int64
		lastTimestamp  int64
		dist           = distribution{bounds: make([]float64, 0, 20), values: make([]uint64, 0, 20)}
		skip           = false
	)
	// We assume that all series belonging to the histogram are sequential. Consume series
	// until we hit a new metric.
Loop:
	for i, s := range samples {
		e, ok, err := b.series.get(ctx, s.Ref)
		if err != nil {
			return nil, 0, samples, err
		}
		if !ok {
			consumed++
			// TODO(fabxc): increment metric.
			continue
		}
		name := e.lset.Get("__name__")
		// The series matches if it has the same base name, the remainder is a valid histogram suffix,
		// and the labels aside from the le and __name__ label match up.
		if !strings.HasPrefix(name, baseName) || !histogramLabelsEqual(e.lset, matchLset) {
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

		rt, v, ok := b.series.getResetAdjusted(s.Ref, s.T, s.V)

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
		// If a series appeared for the first time, we won't get a valid reset timestamp yet.
		// This may happen if the histogram is entirely new or if new series appeared through bucket changes.
		// We skip the entire histogram sample in this case.
		if !ok {
			skip = true
		}
		consumed++
	}
	// Don't emit a sample if we explicitly skip it or no reset timestamp was set because the
	// count series was missing.
	if skip || resetTimestamp == 0 {
		return nil, 0, samples[consumed:], nil
	}
	// We do not assume that the buckets in the sample batch are in order, so we sort them again here.
	// The code below relies on this to convert between Prometheus's and the output's bucketing approaches.
	sort.Sort(&dist)
	// Reuse slices we already populated to build final bounds and values.
	var (
		values  = dist.values[:0]
		prevVal uint64
	)
	for i := range dist.bounds {
		val := dist.values[i] - prevVal
		prevVal = dist.values[i]
		values = append(values, val)
	}
	histogram := &metric_pb.DoubleHistogramDataPoint{
		Count:          uint64(count),
		Sum:            sum,
		BucketCounts:   values,
		ExplicitBounds: dist.bounds[:len(dist.bounds)-1],
	}
	return histogram, resetTimestamp, samples[consumed:], nil
}

// histogramLabelsEqual checks whether two label sets for a histogram series are equal aside from their
// le and __name__ labels.
func histogramLabelsEqual(a, b tsdbLabels.Labels) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i].Name == "le" || a[i].Name == "__name__" {
			i++
			continue
		}
		if b[j].Name == "le" || b[j].Name == "__name__" {
			j++
			continue
		}
		if a[i] != b[j] {
			return false
		}
		i++
		j++
	}
	// Consume trailing le and __name__ labels so the check below passes correctly.
	for i < len(a) {
		if a[i].Name == "le" || a[i].Name == "__name__" {
			i++
			continue
		}
		break
	}
	for j < len(b) {
		if b[j].Name == "le" || b[j].Name == "__name__" {
			j++
			continue
		}
		break
	}
	// If one label set still has labels left, they are not equal.
	return i == len(a) && j == len(b)
}

func monotonicIntegerPoint(labels []*common_pb.StringKeyValue, start, end int64, value float64) *metric_pb.IntSum {
	integer := &metric_pb.IntDataPoint{
		Labels:            labels,
		StartTimeUnixNano: getNanos(start),
		TimeUnixNano:      getNanos(end),
		Value:             int64(value + 0.5),
	}
	return &metric_pb.IntSum{
		IsMonotonic:            true,
		AggregationTemporality: otlpCUMULATIVE,
		DataPoints:             []*metric_pb.IntDataPoint{integer},
	}
}

func monotonicDoublePoint(labels []*common_pb.StringKeyValue, start, end int64, value float64) *metric_pb.DoubleSum {
	double := &metric_pb.DoubleDataPoint{
		Labels:            labels,
		StartTimeUnixNano: getNanos(start),
		TimeUnixNano:      getNanos(end),
		Value:             value,
	}
	return &metric_pb.DoubleSum{
		IsMonotonic:            true,
		AggregationTemporality: otlpCUMULATIVE,
		DataPoints:             []*metric_pb.DoubleDataPoint{double},
	}
}

func intGauge(labels []*common_pb.StringKeyValue, ts int64, value float64) *metric_pb.IntGauge {
	integer := &metric_pb.IntDataPoint{
		Labels:       labels,
		TimeUnixNano: getNanos(ts),
		Value:        int64(value + 0.5),
	}
	return &metric_pb.IntGauge{
		DataPoints: []*metric_pb.IntDataPoint{integer},
	}
}

func doubleGauge(labels []*common_pb.StringKeyValue, ts int64, value float64) *metric_pb.DoubleGauge {
	double := &metric_pb.DoubleDataPoint{
		Labels:       labels,
		TimeUnixNano: getNanos(ts),
		Value:        value,
	}
	return &metric_pb.DoubleGauge{
		DataPoints: []*metric_pb.DoubleDataPoint{double},
	}
}
