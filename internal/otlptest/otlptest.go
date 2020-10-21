package otlptest

import (
	"context"
	"fmt"
	"time"

	otlppb "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/collector/metrics/v1"
	otlpcommon "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/common/v1"
	otlpmetrics "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/metrics/v1"
	otlpresource "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/resource/v1"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/metadata"
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

func IntSumCumulative(name, desc, unit string, idps ...*otlpmetrics.IntDataPoint) *otlpmetrics.Metric {

	return &otlpmetrics.Metric{
		Name:        name,
		Description: desc,
		Unit:        unit,
		Data: &otlpmetrics.Metric_IntSum{
			IntSum: &otlpmetrics.IntSum{
				AggregationTemporality: otlpmetrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				IsMonotonic:            false,
				DataPoints:             idps,
			},
		},
	}
}

func IntSumCumulativeMonotonic(name, desc, unit string, idps ...*otlpmetrics.IntDataPoint) *otlpmetrics.Metric {
	return &otlpmetrics.Metric{
		Name:        name,
		Description: desc,
		Unit:        unit,
		Data: &otlpmetrics.Metric_IntSum{
			IntSum: &otlpmetrics.IntSum{
				AggregationTemporality: otlpmetrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				IsMonotonic:            true,
				DataPoints:             idps,
			},
		},
	}
}

func IntDataPoint(labels []*otlpcommon.StringKeyValue, start, end time.Time, value int64) *otlpmetrics.IntDataPoint {
	return &otlpmetrics.IntDataPoint{
		Labels:            labels,
		StartTimeUnixNano: uint64(start.UnixNano()),
		TimeUnixNano:      uint64(end.UnixNano()),
		Value:             value,
	}
}

func IntGauge(name, desc, unit string, idps ...*otlpmetrics.IntDataPoint) *otlpmetrics.Metric {
	return &otlpmetrics.Metric{
		Name:        name,
		Description: desc,
		Unit:        unit,
		Data: &otlpmetrics.Metric_IntGauge{
			IntGauge: &otlpmetrics.IntGauge{
				DataPoints: idps,
			},
		},
	}
}

func DoubleSumCumulative(name, desc, unit string, ddps ...*otlpmetrics.DoubleDataPoint) *otlpmetrics.Metric {

	return &otlpmetrics.Metric{
		Name:        name,
		Description: desc,
		Unit:        unit,
		Data: &otlpmetrics.Metric_DoubleSum{
			DoubleSum: &otlpmetrics.DoubleSum{
				AggregationTemporality: otlpmetrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				IsMonotonic:            false,
				DataPoints:             ddps,
			},
		},
	}
}

func DoubleSumCumulativeMonotonic(name, desc, unit string, ddps ...*otlpmetrics.DoubleDataPoint) *otlpmetrics.Metric {
	return &otlpmetrics.Metric{
		Name:        name,
		Description: desc,
		Unit:        unit,
		Data: &otlpmetrics.Metric_DoubleSum{
			DoubleSum: &otlpmetrics.DoubleSum{
				AggregationTemporality: otlpmetrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				IsMonotonic:            true,
				DataPoints:             ddps,
			},
		},
	}
}

func DoubleDataPoint(labels []*otlpcommon.StringKeyValue, start, end time.Time, value float64) *otlpmetrics.DoubleDataPoint {
	return &otlpmetrics.DoubleDataPoint{
		Labels:            labels,
		StartTimeUnixNano: uint64(start.UnixNano()),
		TimeUnixNano:      uint64(end.UnixNano()),
		Value:             value,
	}
}

func DoubleGauge(name, desc, unit string, ddps ...*otlpmetrics.DoubleDataPoint) *otlpmetrics.Metric {
	return &otlpmetrics.Metric{
		Name:        name,
		Description: desc,
		Unit:        unit,
		Data: &otlpmetrics.Metric_DoubleGauge{
			DoubleGauge: &otlpmetrics.DoubleGauge{
				DataPoints: ddps,
			},
		},
	}
}

type DoubleHistogramBucketStruct struct {
	Boundary float64
	Count    uint64
}

func DoubleHistogramBucket(boundary float64, count uint64) DoubleHistogramBucketStruct {
	return DoubleHistogramBucketStruct{
		Boundary: boundary,
		Count:    count,
	}
}

func DoubleHistogramDataPoint(labels []*otlpcommon.StringKeyValue, start, end time.Time, sum float64, total uint64, buckets ...DoubleHistogramBucketStruct) *otlpmetrics.DoubleHistogramDataPoint {
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
	return &otlpmetrics.DoubleHistogramDataPoint{
		Labels:            labels,
		StartTimeUnixNano: uint64(start.UnixNano()),
		TimeUnixNano:      uint64(end.UnixNano()),
		Sum:               sum,
		Count:             total,
		BucketCounts:      counts,
		ExplicitBounds:    bounds,
	}
}

func DoubleHistogramCumulative(name, desc, unit string, idps ...*otlpmetrics.DoubleHistogramDataPoint) *otlpmetrics.Metric {
	if len(idps) == 0 {
		idps = []*otlpmetrics.DoubleHistogramDataPoint{}
	}
	return &otlpmetrics.Metric{
		Name:        name,
		Description: desc,
		Unit:        unit,
		Data: &otlpmetrics.Metric_DoubleHistogram{
			DoubleHistogram: &otlpmetrics.DoubleHistogram{
				AggregationTemporality: otlpmetrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				DataPoints:             idps,
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
	kind metadata.Kind,
	// monotonic is meaningful for both DELTA and CUMULATIVE, not
	// GAUGE, and only when represented as a scalar value.  This
	// information is not conveyed via OTLP-v0.5 for the histogram
	// representation.
	monotonic bool,
	// point is one of IntDataPoint, DoubleDataPoint,
	// IntHistogram, DoubleHistogram.
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
				case *otlpmetrics.Metric_IntGauge:
					if t.IntGauge != nil {
						kind := metadata.GAUGE
						for _, p := range t.IntGauge.DataPoints {
							vs.pointCount++
							noticeError(m, visitor(res, m.Name, kind, false, p))
						}
						continue
					}
				case *otlpmetrics.Metric_DoubleGauge:
					if t.DoubleGauge != nil {
						kind := metadata.GAUGE
						for _, p := range t.DoubleGauge.DataPoints {
							vs.pointCount++
							noticeError(m, visitor(res, m.Name, kind, false, p))
						}
						continue
					}
				case *otlpmetrics.Metric_IntSum:
					if t.IntSum != nil {
						kind := toModelKind(t.IntSum.AggregationTemporality)
						for _, p := range t.IntSum.DataPoints {
							vs.pointCount++
							noticeError(m, visitor(res, m.Name, kind, t.IntSum.IsMonotonic, p))
						}
						continue
					}
				case *otlpmetrics.Metric_DoubleSum:
					if t.DoubleSum != nil {
						kind := toModelKind(t.DoubleSum.AggregationTemporality)
						for _, p := range t.DoubleSum.DataPoints {
							vs.pointCount++
							noticeError(m, visitor(res, m.Name, kind, t.DoubleSum.IsMonotonic, p))
						}
						continue
					}
				case *otlpmetrics.Metric_IntHistogram:
					if t.IntHistogram != nil {
						kind := toModelKind(t.IntHistogram.AggregationTemporality)
						for _, p := range t.IntHistogram.DataPoints {
							vs.pointCount++
							noticeError(m, visitor(res, m.Name, kind, false, p))
						}
						continue
					}
				case *otlpmetrics.Metric_DoubleHistogram:
					if t.DoubleHistogram != nil {
						kind := toModelKind(t.DoubleHistogram.AggregationTemporality)
						for _, p := range t.DoubleHistogram.DataPoints {
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

func toModelKind(temp otlpmetrics.AggregationTemporality) metadata.Kind {
	if temp == otlpmetrics.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA {
		return metadata.DELTA
	} else if temp == otlpmetrics.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE {
		return metadata.CUMULATIVE
	}
	// This is an error case, there are only two valid
	// temporalities.  Fall back to GAUGE instead of producing an
	// error.
	return metadata.GAUGE
}
