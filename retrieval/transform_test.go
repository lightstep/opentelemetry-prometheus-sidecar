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
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/internal/otlptest"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/internal/promtest"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/prometheus/tsdb/record"
	"github.com/stretchr/testify/require"
	common_pb "go.opentelemetry.io/proto/otlp/common/v1"
	metric_pb "go.opentelemetry.io/proto/otlp/metrics/v1"
	messagediff "gopkg.in/d4l3k/messagediff.v1"
)

type metadataEntry = config.MetadataEntry

// seriesMap implements seriesGetter.
type seriesMap map[uint64]labels.Labels

func TestSampleBuilder(t *testing.T) {
	type (
		DoubleHistogramBucketStruct = otlptest.DoubleHistogramBucketStruct
	)
	var (
		IntSumCumulativeMonotonic    = otlptest.IntSumCumulativeMonotonic
		IntGauge                     = otlptest.IntGauge
		IntDataPoint                 = otlptest.IntDataPoint
		DoubleSumCumulativeMonotonic = otlptest.DoubleSumCumulativeMonotonic
		DoubleGauge                  = otlptest.DoubleGauge
		DoubleDataPoint              = otlptest.DoubleDataPoint
		DoubleHistogramDataPoint     = otlptest.DoubleHistogramDataPoint
		DoubleHistogramCumulative    = otlptest.DoubleHistogramCumulative
		DoubleHistogramBucket        = otlptest.DoubleHistogramBucket
		Labels                       = otlptest.Labels
		Label                        = otlptest.Label

		DoubleCounterPoint = func(
			labels []*common_pb.StringKeyValue,
			name string,
			start, end time.Time,
			value float64,
		) *metric_pb.Metric {
			return DoubleSumCumulativeMonotonic(
				name, "", "",
				DoubleDataPoint(
					labels,
					start,
					end,
					value,
				),
			)
		}
		DoubleGaugePoint = func(
			labels []*common_pb.StringKeyValue,
			name string,
			end time.Time,
			value float64,
		) *metric_pb.Metric {
			return DoubleGauge(
				name, "", "",
				DoubleDataPoint(
					labels,
					time.Unix(0, 0),
					end,
					value,
				),
			)
		}
		IntCounterPoint = func(
			labels []*common_pb.StringKeyValue,
			name string,
			start, end time.Time,
			value int64,
		) *metric_pb.Metric {
			return IntSumCumulativeMonotonic(
				name, "", "",
				IntDataPoint(
					labels,
					start,
					end,
					value,
				),
			)
		}
		IntGaugePoint = func(
			labels []*common_pb.StringKeyValue,
			name string,
			end time.Time,
			value int64,
		) *metric_pb.Metric {
			return IntGauge(
				name, "", "",
				IntDataPoint(
					labels,
					time.Unix(0, 0),
					end,
					value,
				),
			)
		}

		DoubleHistogramPoint = func(
			labels []*common_pb.StringKeyValue,
			name string,
			start, end time.Time,
			sum float64, count uint64,
			buckets ...DoubleHistogramBucketStruct,
		) *metric_pb.Metric {
			return DoubleHistogramCumulative(
				name, "", "",
				DoubleHistogramDataPoint(
					labels,
					start,
					end,
					sum,
					count,
					buckets...,
				),
			)
		}
	)

	isNotFound := func(err error) bool {
		return errors.Is(err, errSeriesNotFound)
	}
	isMissingMetadata := func(err error) bool {
		return errors.Is(err, errSeriesMissingMetadata)
	}
	isMissingHistogramMetadata := func(err error) bool {
		return errors.Is(err, errHistogramMetadataMissing)
	}

	// Note: Be aware that the *resulting* points' labels will be arranged
	// *alphabetically*.
	cases := []struct {
		name          string
		series        seriesMap
		metadata      MetadataGetter
		metricsPrefix string
		input         []record.RefSample
		result        []*metric_pb.Metric
		errors        []func(error) bool
		failures      map[string]bool
	}{
		{
			name: "basics",
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "a", "1", "__name__", "metric1"),
				2: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric2"),
				3: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "labelnum_ok",
					"a", "1", "b", "2", "c", "3", "d", "4", "e", "5", "f", "6", "g", "7", "h", "8", "i", "9", "j", "10"),
				4: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "labelnum_11k",
					"a", "1", "b", "2", "c", "3", "d", "4", "e", "5", "f", "6", "g", "7", "h", "8", "i", "9", "j", "10", "k", "11"),
				5: labels.FromStrings("job", "job2", "instance", "instance1", "__name__", "resource_from_metric", "metric_label", "aaa", "a", "1"),
				6: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric3"),
				7: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric4"),
				8: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric5"),
				9: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric6"),
			},
			metadata: promtest.MetadataMap{
				// Gauge as double.
				"job1/instance1/metric1": &metadataEntry{Metric: "metric1", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE},
				// Gauge as integer.
				"job1/instance1/metric3": &metadataEntry{Metric: "metric3", MetricType: textparse.MetricTypeGauge, ValueType: config.INT64},
				// Gauge as default value type (double).
				"job1/instance1/metric5": &metadataEntry{Metric: "metric5", MetricType: textparse.MetricTypeGauge},
				// Counter as double.
				"job1/instance1/metric2": &metadataEntry{Metric: "metric2", MetricType: textparse.MetricTypeCounter, ValueType: config.DOUBLE},
				// Counter as integer.
				"job1/instance1/metric4": &metadataEntry{Metric: "metric4", MetricType: textparse.MetricTypeCounter, ValueType: config.INT64},
				// Counter as default value type (double).
				"job1/instance1/metric6":              &metadataEntry{Metric: "metric6", MetricType: textparse.MetricTypeCounter},
				"job1/instance1/labelnum_ok":          &metadataEntry{Metric: "labelnum_ok", MetricType: textparse.MetricTypeUnknown, ValueType: config.DOUBLE},
				"job1/instance1/labelnum_11k":         &metadataEntry{Metric: "labelnum_11k", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE},
				"job2/instance1/resource_from_metric": &metadataEntry{Metric: "resource_from_metric", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE},
			},
			input: []record.RefSample{
				{Ref: 2, T: 2000, V: 5.5}, // 0
				{Ref: 2, T: 3000, V: 8},
				{Ref: 2, T: 4000, V: 9},
				{Ref: 2, T: 5000, V: 3},
				{Ref: 1, T: 1000, V: 200},
				{Ref: 3, T: 3000, V: 1}, // 5
				{Ref: 4, T: 4000, V: 2},
				{Ref: 5, T: 1000, V: 200},
				{Ref: 6, T: 8000, V: 12.5},
				{Ref: 7, T: 6000, V: 1},
				{Ref: 7, T: 7000, V: 3.5}, // 10
				{Ref: 8, T: 8000, V: 22.5},
				{Ref: 9, T: 8000, V: 3},
				{Ref: 9, T: 9000, V: 4},
			},
			result: []*metric_pb.Metric{
				DoubleCounterPoint( // 1: second point in series, first reported.
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric2",
					time.Unix(2, 0),
					time.Unix(2, 0),
					0,
				),
				DoubleCounterPoint( // 1: second point in series, first reported.
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric2",
					time.Unix(2, 0),
					time.Unix(3, 0),
					2.5,
				),
				DoubleCounterPoint( // 2: third point in series, second reported.
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric2",
					time.Unix(2, 0),
					time.Unix(4, 0),
					3.5,
				),
				DoubleCounterPoint( // 3: A reset
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric2",
					time.Unix(5, 0),
					time.Unix(5, 0),
					3,
				),
				DoubleGaugePoint( // 4: A double Gauge
					Labels(
						Label("a", "1"),
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric1",
					time.Unix(1, 0),
					200,
				),
				DoubleGaugePoint( // 5: A double gauge w/ 10 keys
					Labels(
						Label("a", "1"),
						Label("b", "2"),
						Label("c", "3"),
						Label("d", "4"),
						Label("e", "5"),
						Label("f", "6"),
						Label("g", "7"),
						Label("h", "8"),
						Label("i", "9"),
						Label("instance", "instance1"),
						Label("j", "10"),
						Label("job", "job1"),
					),
					"labelnum_ok",
					time.Unix(3, 0),
					1,
				),
				DoubleGaugePoint( // 6: A double gauge w/ 11 keys
					Labels(
						Label("a", "1"),
						Label("b", "2"),
						Label("c", "3"),
						Label("d", "4"),
						Label("e", "5"),
						Label("f", "6"),
						Label("g", "7"),
						Label("h", "8"),
						Label("i", "9"),
						Label("instance", "instance1"),
						Label("j", "10"),
						Label("job", "job1"),
						Label("k", "11"),
					),
					"labelnum_11k",
					time.Unix(4, 0),
					2,
				),
				DoubleGaugePoint( // 7
					// A double gauge w/ 2 labels
					Labels(
						Label("a", "1"),
						Label("instance", "instance1"),
						Label("job", "job2"),
						Label("metric_label", "aaa"),
					),
					"resource_from_metric",
					time.Unix(1, 0),
					200,
				),
				IntGaugePoint( // 8
					// An integer gauge: rounding from 12.5 to 13
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric3",
					time.Unix(8, 0),
					13,
				),
				IntCounterPoint( // 9
					// An integer counter.
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric4",
					time.Unix(6, 0),
					time.Unix(6, 0),
					0,
				),
				IntCounterPoint( // 10
					// An integer counter.
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric4",
					time.Unix(6, 0),
					time.Unix(7, 0),
					3,
				),
				DoubleGaugePoint( // 11
					// A double gauge.
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric5",
					time.Unix(8, 0),
					22.5),
				DoubleCounterPoint( // 12
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric6",
					time.Unix(8, 0),
					time.Unix(8, 0),
					0,
				),
				DoubleCounterPoint( // 13
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric6",
					time.Unix(8, 0),
					time.Unix(9, 0),
					1,
				),
			},
		},
		// Various cases where we drop series due to absence of additional information.
		{
			name: "absence of data",
			metadata: promtest.MetadataMap{
				"job1/instance1/metric1": &metadataEntry{Metric: "metric1", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE},
			},
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance_notfound", "__name__", "metric1"),
				2: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric_notfound"),
				3: labels.FromStrings("job", "job1", "instance", "instance_noresource", "__name__", "metric1"),
			},
			input: []record.RefSample{
				{Ref: 1, T: 1000, V: 1},
				{Ref: 2, T: 2000, V: 2},
				{Ref: 3, T: 3000, V: 3},
			},
			result: []*metric_pb.Metric{nil, nil, nil},
			errors: []func(err error) bool{
				isMissingMetadata,
				isMissingMetadata,
				isMissingMetadata,
			},
			failures: map[string]bool{
				"metadata_missing/metric1":         true,
				"metadata_missing/metric_notfound": true,
			},
		},
		// Summary metrics.
		{
			name: "summary",
			metadata: promtest.MetadataMap{
				"job1/instance1/metric1": &metadataEntry{Metric: "metric1", MetricType: textparse.MetricTypeSummary, ValueType: config.DOUBLE},
			},
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_sum"),
				2: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1", "quantile", "0.5"),
				3: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_count"),
				4: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1", "quantile", "0.9"),
			},
			input: []record.RefSample{
				{Ref: 1, T: 1000, V: 1},
				{Ref: 1, T: 1500, V: 1},
				{Ref: 2, T: 2000, V: 2},
				{Ref: 3, T: 3000, V: 3},
				{Ref: 3, T: 3500, V: 4},
				{Ref: 4, T: 4000, V: 4},
			},
			result: []*metric_pb.Metric{
				DoubleCounterPoint(
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric1_sum",
					time.Unix(1, 0),
					time.Unix(1, 0),
					0,
				),
				DoubleCounterPoint(
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric1_sum",
					time.Unix(1, 0),
					time.Unix(1, int64(500*time.Millisecond)),
					0,
				),
				DoubleGaugePoint(
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
						Label("quantile", "0.5"),
					),
					"metric1",
					time.Unix(2, 0),
					2,
				),
				IntCounterPoint(
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric1_count",
					time.Unix(3, 0),
					time.Unix(3, 0),
					0,
				),
				IntCounterPoint(
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric1_count",
					time.Unix(3, 0),
					time.Unix(3, int64(500*time.Millisecond)),
					1,
				),
				DoubleGaugePoint(
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
						Label("quantile", "0.9"),
					),
					"metric1",
					time.Unix(4, 0),
					4,
				),
			},
		},
		// Histogram.
		{
			name: "histogram",
			metadata: promtest.MetadataMap{
				"job1/instance1/metric1":         &metadataEntry{Metric: "metric1", MetricType: textparse.MetricTypeHistogram, ValueType: config.DOUBLE},
				"job1/instance1/metric1_a_count": &metadataEntry{Metric: "metric1_a_count", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE},
			},
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_sum"),
				2: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_count"),
				3: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_bucket", "le", "0.1"),
				4: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_bucket", "le", "0.5"),
				5: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_bucket", "le", "1"),
				6: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_bucket", "le", "2.5"),
				7: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_bucket", "le", "+Inf"),
				// Add another series that only deviates by having an extra label. We must properly detect a new histogram.
				// This is an discouraged but possible case of metric labeling.
				8: labels.FromStrings("job", "job1", "instance", "instance1", "a", "b", "__name__", "metric1_sum"),
				9: labels.FromStrings("job", "job1", "instance", "instance1", "a", "b", "__name__", "metric1_count"),
				// Series that triggers more edge cases.
				10: labels.FromStrings("job", "job1", "instance", "instance1", "a", "b", "__name__", "metric1_a_count"),
			},
			input: []record.RefSample{
				// Mix up order of the series to test bucket sorting.
				// First sample set, should be skipped by reset handling.
				{Ref: 3, T: 1000, V: 2},    // 0.1
				{Ref: 5, T: 1000, V: 6},    // 1
				{Ref: 6, T: 1000, V: 8},    // 2.5
				{Ref: 7, T: 1000, V: 10},   // inf
				{Ref: 1, T: 1000, V: 55.1}, // sum
				{Ref: 4, T: 1000, V: 5},    // 0.5
				{Ref: 2, T: 1000, V: 10},   // count
				// Second sample set should actually be emitted.
				{Ref: 2, T: 2000, V: 21},    // count
				{Ref: 3, T: 2000, V: 4},     // 0.1
				{Ref: 6, T: 2000, V: 15},    // 2.5
				{Ref: 5, T: 2000, V: 11},    // 1
				{Ref: 1, T: 2000, V: 123.4}, // sum
				{Ref: 7, T: 2000, V: 21},    // inf
				{Ref: 4, T: 2000, V: 9},     // 0.5
				// New histogram without actual buckets – should still work.
				{Ref: 8, T: 1000, V: 100},
				{Ref: 9, T: 1000, V: 10},
				{Ref: 8, T: 2000, V: 115},
				{Ref: 9, T: 2000, V: 13},
				// New metric that actually matches the base name but the suffix is more more than a valid histogram suffix.
				{Ref: 10, T: 1000, V: 3},
			},
			result: []*metric_pb.Metric{
				DoubleHistogramPoint( // 0:
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric1",
					time.Unix(1, 0),
					time.Unix(1, 0),
					0,
					0,
					DoubleHistogramBucket(0.1, 0),
					DoubleHistogramBucket(0.5, 0),
					DoubleHistogramBucket(1, 0),
					DoubleHistogramBucket(2.5, 0),
					DoubleHistogramBucket(math.Inf(+1), 0),
				),
				DoubleHistogramPoint( // 1:
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric1",
					time.Unix(1, 0),
					time.Unix(2, 0),
					float64(123.4)-float64(55.1),
					21-10,
					DoubleHistogramBucket(0.1, 2),
					DoubleHistogramBucket(0.5, 2),
					DoubleHistogramBucket(1, 1),
					DoubleHistogramBucket(2.5, 2),
					DoubleHistogramBucket(math.Inf(+1), 4),
				),
				DoubleHistogramPoint( // 2: histogram w/ no buckets
					Labels(
						Label("a", "b"),
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric1",
					time.Unix(1, 0),
					time.Unix(1, 0),
					0,
					0,
				),
				DoubleHistogramPoint( // 3: histogram w/ no buckets
					Labels(
						Label("a", "b"),
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric1",
					time.Unix(1, 0),
					time.Unix(2, 0),
					15,
					3,
				),
				DoubleGaugePoint( // 4: not a histogram
					Labels(
						Label("a", "b"),
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric1_a_count",
					time.Unix(1, 0),
					3,
				),
			},
		},
		// Customized metric prefix.
		{
			name: "custom prefix",
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "a", "1", "__name__", "metric1"),
			},
			metadata: promtest.MetadataMap{
				"job1/instance1/metric1": &metadataEntry{Metric: "metric1", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE},
			},
			metricsPrefix: "test.otel.io/",
			input: []record.RefSample{
				{Ref: 1, T: 1000, V: 200},
			},
			result: []*metric_pb.Metric{
				DoubleGaugePoint(
					Labels(
						Label("a", "1"),
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"test.otel.io/metric1",
					time.Unix(1, 0),
					200,
				),
			},
		},
		// Any counter metric with the _total suffix should be treated as normal if metadata
		// can be found for the original metric name.
		{
			name: "total not distribution",
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "a", "1", "__name__", "metric1_total"),
			},
			metadata: promtest.MetadataMap{
				"job1/instance1/metric1_total": &metadataEntry{Metric: "metric1_total", MetricType: textparse.MetricTypeCounter, ValueType: config.DOUBLE},
			},
			input: []record.RefSample{
				{Ref: 1, T: 2000, V: 5.5},
				{Ref: 1, T: 3000, V: 8},
			},
			result: []*metric_pb.Metric{
				DoubleCounterPoint(
					Labels(
						Label("a", "1"),
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric1_total",
					time.Unix(2, 0),
					time.Unix(2, 0),
					0,
				),
				DoubleCounterPoint(
					Labels(
						Label("a", "1"),
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric1_total",
					time.Unix(2, 0),
					time.Unix(3, 0),
					2.5,
				),
			},
		},
		// Any counter metric with the _total suffix should fail over to the metadata for
		// the metric with the _total suffix removed while reporting the metric with the
		// _total suffix removed in the metric name as well.
		{
			name: "only total distribution counter",
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "a", "1", "__name__", "metric1_total"),
			},
			metadata: promtest.MetadataMap{
				"job1/instance1/metric1": &metadataEntry{Metric: "metric1", MetricType: textparse.MetricTypeCounter, ValueType: config.DOUBLE},
			},
			input: []record.RefSample{
				{Ref: 1, T: 2000, V: 5.5},
				{Ref: 1, T: 3000, V: 8},
			},
			result: []*metric_pb.Metric{
				DoubleCounterPoint(
					Labels(
						Label("a", "1"),
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric1",
					time.Unix(2, 0),
					time.Unix(2, 0),
					0,
				),
				DoubleCounterPoint(
					Labels(
						Label("a", "1"),
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric1",
					time.Unix(2, 0),
					time.Unix(3, 0),
					2.5,
				),
			},
		},
		// Any non-counter metric with the _total suffix should fail over to the metadata
		// for the metric with the _total suffix removed while reporting the metric with
		// the original name.
		{
			name: "only total distribution gauge",
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "a", "1", "__name__", "metric1_total"),
			},
			metadata: promtest.MetadataMap{
				"job1/instance1/metric1": &metadataEntry{Metric: "metric1", MetricType: textparse.MetricTypeGauge, ValueType: config.DOUBLE},
			},
			input: []record.RefSample{
				{Ref: 1, T: 3000, V: 8},
			},
			result: []*metric_pb.Metric{
				DoubleGaugePoint(
					Labels(
						Label("a", "1"),
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric1_total",
					time.Unix(3, 0),
					8,
				),
			},
		},
		// Samples with a NaN value should be dropped.
		{
			name: "NaN value",
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1_count"),
			},
			metadata: promtest.MetadataMap{
				"job1/instance1/metric1": &metadataEntry{Metric: "metric1_count", MetricType: textparse.MetricTypeSummary, ValueType: config.DOUBLE},
			},
			input: []record.RefSample{
				{Ref: 1, T: 4000, V: math.NaN()},
			},
			result: []*metric_pb.Metric{
				nil, // due to NaN
			},
		},
		// Samples with a NaN value and a cumulative
		{
			name: "NaN and cumulative",
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "__name__", "metric1"),
			},
			metadata: promtest.MetadataMap{
				"job1/instance1/metric1": &metadataEntry{Metric: "metric1", MetricType: textparse.MetricTypeCounter, ValueType: config.INT64},
			},
			input: []record.RefSample{
				// A first non-NaN sample is necessary to avoid false-positives, since the
				// first result will always be nil due to reset timestamp handling.
				{Ref: 1, T: 2000, V: 5},
				{Ref: 1, T: 4000, V: math.NaN()},
				{Ref: 1, T: 5000, V: 9},
			},
			result: []*metric_pb.Metric{
				IntCounterPoint(
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric1",
					time.Unix(2, 0),
					time.Unix(2, 0),
					0,
				),
				nil, // due to NaN
				IntCounterPoint(
					Labels(
						Label("instance", "instance1"),
						Label("job", "job1"),
					),
					"metric1",
					time.Unix(2, 0),
					time.Unix(5, 0),
					4,
				),
			},
		},
		// Gauge/Histogram metadata conflict
		{
			name: "gauge not histogram",
			series: seriesMap{
				1: labels.FromStrings("job", "job1", "instance", "instance1", "a", "1", "__name__", "metric1"),
			},
			metadata: promtest.MetadataMap{
				"job1/instance1/metric1": &metadataEntry{Metric: "metric1", MetricType: textparse.MetricTypeHistogram, ValueType: config.DOUBLE},
			},
			input: []record.RefSample{
				{Ref: 1, T: 2000, V: 5.5},
			},
			result: []*metric_pb.Metric{
				nil,
			},
			errors: []func(error) bool{
				isMissingHistogramMetadata,
			},
		},
		// Missing series ref
		{
			name:     "no series ref",
			series:   seriesMap{},
			metadata: promtest.MetadataMap{},
			input: []record.RefSample{
				{Ref: 1, T: 2000, V: 5.5},
			},
			result: []*metric_pb.Metric{
				nil,
			},
			errors: []func(error) bool{
				isNotFound,
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, c := range cases {
		t.Run(fmt.Sprintf("Test case %s", c.name),
			func(t *testing.T) {
				var s *metric_pb.Metric
				var result []*metric_pb.Metric

				testFailing := testFailingReporter{}
				series := newSeriesCache(nil, "", nil, nil, c.metadata, c.metricsPrefix, nil, testFailing)
				for ref, s := range c.series {
					series.set(ctx, ref, s, 0)
				}

				b := &sampleBuilder{series: series}

				for k := 0; len(c.input) > 0; k++ {
					var err error
					s, c.input, err = b.next(context.Background(), c.input)

					result = append(result, s)

					if c.errors == nil {
						require.NoError(t, err)
						continue
					}

					require.True(t, c.errors[k](err), "For %d %v", k, err)
				}

				if diff, equal := messagediff.PrettyDiff(c.result, result); !equal {
					t.Errorf("unexpected result:\n%v", diff)
				}

				if len(result) != len(c.result) {
					t.Errorf("mismatching count %d of received samples, want %d", len(result), len(c.result))
				}

				if c.failures == nil {
					c.failures = testFailingReporter{}
				}

				require.EqualValues(t, c.failures, testFailing)
			})
	}
}
