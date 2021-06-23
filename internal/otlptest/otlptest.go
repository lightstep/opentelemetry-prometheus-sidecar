package otlptest

import (
	"context"
	"fmt"
	"time"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	otlppb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	otlpcommon "go.opentelemetry.io/proto/otlp/common/v1"
	otlpmetrics "go.opentelemetry.io/proto/otlp/metrics/v1"
	otlpresource "go.opentelemetry.io/proto/otlp/resource/v1"
)

var (
	ErrEmptyMetricName  = fmt.Errorf("empty metric name")
	ErrInvalidPointKind = fmt.Errorf("unrecognized data point type")
)

func ExportRequest(rms ...*otlpmetrics.ResourceMetrics) *otlppb.ExportMetricsServiceRequest {
	return &otlppb.ExportMetricsServiceRequest{
		ResourceMetrics: rms,
	}
}

func ResourceMetrics(resource *otlpresource.Resource, ilms ...*otlpmetrics.InstrumentationLibraryMetrics) *otlpmetrics.ResourceMetrics {
	return &otlpmetrics.ResourceMetrics{
		Resource:                      resource,
		InstrumentationLibraryMetrics: ilms,
	}
}

func KeyValue(k, v string) *otlpcommon.KeyValue {
	return &otlpcommon.KeyValue{
		Key: k,
		Value: &otlpcommon.AnyValue{
			Value: &otlpcommon.AnyValue_StringValue{
				StringValue: v,
			},
		},
	}
}

func Resource(kvs ...*otlpcommon.KeyValue) *otlpresource.Resource {
	return &otlpresource.Resource{
		Attributes: kvs,
	}
}

func InstrumentationLibrary(name, version string) *otlpcommon.InstrumentationLibrary {
	return &otlpcommon.InstrumentationLibrary{
		Name:    name,
		Version: version,
	}
}

func InstrumentationLibraryMetrics(lib *otlpcommon.InstrumentationLibrary, ms ...*otlpmetrics.Metric) *otlpmetrics.InstrumentationLibraryMetrics {
	return &otlpmetrics.InstrumentationLibraryMetrics{
		InstrumentationLibrary: lib,
		Metrics:                ms,
	}
}

func Label(key, value string) *otlpcommon.StringKeyValue {
	return &otlpcommon.StringKeyValue{
		Key:   key,
		Value: value,
	}
}

func Labels(kvs ...*otlpcommon.StringKeyValue) []*otlpcommon.StringKeyValue {
	if len(kvs) == 0 {
		return []*otlpcommon.StringKeyValue{}
	}
	return kvs
}

func ResourceLabels(kvs ...*otlpcommon.KeyValue) []*otlpcommon.KeyValue {
	if len(kvs) == 0 {
		return []*otlpcommon.KeyValue{}
	}
	return kvs
}

func IntDataPoint(labels []*otlpcommon.StringKeyValue, start, end time.Time, value int64) *otlpmetrics.NumberDataPoint {
	return &otlpmetrics.NumberDataPoint{
		Labels:            labels,
		StartTimeUnixNano: uint64(start.UnixNano()),
		TimeUnixNano:      uint64(end.UnixNano()),
		Value:             &otlpmetrics.NumberDataPoint_AsInt{AsInt: value},
	}
}

func SumCumulative(name, desc, unit string, ddps ...*otlpmetrics.NumberDataPoint) *otlpmetrics.Metric {

	return &otlpmetrics.Metric{
		Name:        name,
		Description: desc,
		Unit:        unit,
		Data: &otlpmetrics.Metric_Sum{
			Sum: &otlpmetrics.Sum{
				AggregationTemporality: otlpmetrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				IsMonotonic:            false,
				DataPoints:             ddps,
			},
		},
	}
}

func SumCumulativeMonotonic(name, desc, unit string, ddps ...*otlpmetrics.NumberDataPoint) *otlpmetrics.Metric {
	return &otlpmetrics.Metric{
		Name:        name,
		Description: desc,
		Unit:        unit,
		Data: &otlpmetrics.Metric_Sum{
			Sum: &otlpmetrics.Sum{
				AggregationTemporality: otlpmetrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				IsMonotonic:            true,
				DataPoints:             ddps,
			},
		},
	}
}

func DoubleDataPoint(labels []*otlpcommon.StringKeyValue, start, end time.Time, value float64) *otlpmetrics.NumberDataPoint {
	return &otlpmetrics.NumberDataPoint{
		Labels:            labels,
		StartTimeUnixNano: uint64(start.UnixNano()),
		TimeUnixNano:      uint64(end.UnixNano()),
		Value:             &otlpmetrics.NumberDataPoint_AsDouble{AsDouble: value},
	}
}

func Gauge(name, desc, unit string, ddps ...*otlpmetrics.NumberDataPoint) *otlpmetrics.Metric {
	return &otlpmetrics.Metric{
		Name:        name,
		Description: desc,
		Unit:        unit,
		Data: &otlpmetrics.Metric_Gauge{
			Gauge: &otlpmetrics.Gauge{
				DataPoints: ddps,
			},
		},
	}
}

type HistogramBucketStruct struct {
	Boundary float64
	Count    uint64
}

func HistogramBucket(boundary float64, count uint64) HistogramBucketStruct {
	return HistogramBucketStruct{
		Boundary: boundary,
		Count:    count,
	}
}

func HistogramDataPoint(labels []*otlpcommon.StringKeyValue, start, end time.Time, sum float64, total uint64, buckets ...HistogramBucketStruct) *otlpmetrics.HistogramDataPoint {
	blen := 0
	if len(buckets) > 0 {
		blen = len(buckets) - 1
	}
	counts := make([]uint64, len(buckets))
	bounds := make([]float64, blen)
	for i, b := range buckets[:len(bounds)] {
		counts[i] = b.Count
		bounds[i] = b.Boundary
	}
	if len(buckets) > 0 {
		counts[len(buckets)-1] = buckets[len(buckets)-1].Count
	}
	return &otlpmetrics.HistogramDataPoint{
		Labels:            labels,
		StartTimeUnixNano: uint64(start.UnixNano()),
		TimeUnixNano:      uint64(end.UnixNano()),
		Sum:               sum,
		Count:             total,
		BucketCounts:      counts,
		ExplicitBounds:    bounds,
	}
}

func HistogramCumulative(name, desc, unit string, idps ...*otlpmetrics.HistogramDataPoint) *otlpmetrics.Metric {
	if len(idps) == 0 {
		idps = []*otlpmetrics.HistogramDataPoint{}
	}
	return &otlpmetrics.Metric{
		Name:        name,
		Description: desc,
		Unit:        unit,
		Data: &otlpmetrics.Metric_Histogram{
			Histogram: &otlpmetrics.Histogram{
				AggregationTemporality: otlpmetrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				DataPoints:             idps,
			},
		},
	}
}

type SummaryQuantileValueStruct struct {
	Quantile float64
	Value    float64
}

func SummaryQuantileValue(quantile, value float64) SummaryQuantileValueStruct {
	return SummaryQuantileValueStruct{
		Quantile: quantile,
		Value:    value,
	}
}

func SummaryDataPoint(labels []*otlpcommon.StringKeyValue, start, end time.Time, sum float64, count uint64, quantiles ...SummaryQuantileValueStruct) *otlpmetrics.SummaryDataPoint {
	quantileValues := make([]*otlpmetrics.SummaryDataPoint_ValueAtQuantile, 0, len(quantiles))
	for _, value := range quantiles {
		quantileValues = append(quantileValues, &otlpmetrics.SummaryDataPoint_ValueAtQuantile{
			Quantile: value.Quantile,
			Value:    value.Value,
		})
	}
	return &otlpmetrics.SummaryDataPoint{
		Labels:            labels,
		StartTimeUnixNano: uint64(start.UnixNano()),
		TimeUnixNano:      uint64(end.UnixNano()),
		Sum:               sum,
		Count:             count,
		QuantileValues:    quantileValues,
	}
}

func Summary(name, desc, unit string, dps ...*otlpmetrics.SummaryDataPoint) *otlpmetrics.Metric {
	if len(dps) == 0 {
		dps = []*otlpmetrics.SummaryDataPoint{}
	}
	return &otlpmetrics.Metric{
		Name:        name,
		Description: desc,
		Unit:        unit,
		Data: &otlpmetrics.Metric_Summary{
			Summary: &otlpmetrics.Summary{
				DataPoints: dps,
			},
		},
	}
}

// Visitor is used because it takes around 50 lines of code to
// traverse an OTLP request, with looping over Resource,
// Instrumentation Library, the list of Metrics, and six distinct
// types of data point.
type Visitor func(
	// resource describes the process producing telemetry
	resource *otlpresource.Resource,
	// metricName is the name of the metric instrument
	metricName string,
	// kind is the metricdb kind associated with this measurement
	kind config.Kind,
	// monotonic is meaningful for both DELTA and CUMULATIVE, not
	// GAUGE, and only when represented as a scalar value.  This
	// information is not conveyed via OTLP-v0.5 for the histogram
	// representation.
	monotonic bool,
	// point is one of NumberDataPoint, HistogramDataPoint,
	// SummaryDataPoint.
	point interface{},
) error

type VisitorState struct {
	invalidCount int
	pointCount   int
}

func (vs *VisitorState) PointCount() int {
	return vs.pointCount
}

func (vs *VisitorState) InvalidCount() int {
	return vs.invalidCount
}

func (vs *VisitorState) Visit(
	ctx context.Context,
	visitor Visitor,
	metrics ...*otlpmetrics.ResourceMetrics,
) error {
	var firstError error
	noticeError := func(m *otlpmetrics.Metric, err error) {
		if err == nil {
			return
		}
		if firstError == nil {
			firstError = err
		}

		vs.invalidCount++
	}

	for _, rm := range metrics {
		res := rm.Resource
		for _, il := range rm.InstrumentationLibraryMetrics {
			for _, m := range il.Metrics {
				if m.Name == "" {
					noticeError(m, ErrEmptyMetricName)
					continue
				}

				switch t := m.Data.(type) {
				case *otlpmetrics.Metric_Gauge:
					if t.Gauge != nil {
						kind := config.GAUGE
						for _, p := range t.Gauge.DataPoints {
							vs.pointCount++
							noticeError(m, visitor(res, m.Name, kind, false, p))
						}
						continue
					}
				case *otlpmetrics.Metric_Sum:
					if t.Sum != nil {
						kind := toModelKind(t.Sum.AggregationTemporality)
						for _, p := range t.Sum.DataPoints {
							vs.pointCount++
							noticeError(m, visitor(res, m.Name, kind, t.Sum.IsMonotonic, p))
						}
						continue
					}
				case *otlpmetrics.Metric_Histogram:
					if t.Histogram != nil {
						kind := toModelKind(t.Histogram.AggregationTemporality)
						for _, p := range t.Histogram.DataPoints {
							vs.pointCount++
							noticeError(m, visitor(res, m.Name, kind, false, p))
						}
						continue
					}
				}
				noticeError(m, ErrInvalidPointKind)
			}
		}
	}
	return firstError
}

func toModelKind(temp otlpmetrics.AggregationTemporality) config.Kind {
	if temp == otlpmetrics.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA {
		return config.DELTA
	} else if temp == otlpmetrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE {
		return config.CUMULATIVE
	}
	// This is an error case, there are only two valid
	// temporalities.  Fall back to GAUGE instead of producing an
	// error.
	return config.GAUGE
}
