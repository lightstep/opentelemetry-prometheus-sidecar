package otlptest

import (
	"context"
	"fmt"
	"time"

	otlppb "github.com/lightstep/lightstep-prometheus-sidecar/internal/opentelemetry-proto-gen/collector/metrics/v1"
	otlpcommon "github.com/lightstep/lightstep-prometheus-sidecar/internal/opentelemetry-proto-gen/common/v1"
	otlpmetrics "github.com/lightstep/lightstep-prometheus-sidecar/internal/opentelemetry-proto-gen/metrics/v1"
	otlpresource "github.com/lightstep/lightstep-prometheus-sidecar/internal/opentelemetry-proto-gen/resource/v1"
	"github.com/lightstep/lightstep-prometheus-sidecar/metadata"
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

type visitorState struct {
	invalidCount int
}

func (vs *visitorState) visitRequest(
	ctx context.Context,
	request *otlppb.ExportMetricsServiceRequest,
	visitor Visitor,
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

	for _, rm := range request.ResourceMetrics {
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
							noticeError(m, visitor(res, m.Name, kind, false, p))
						}
						continue
					}
				case *otlpmetrics.Metric_DoubleGauge:
					if t.DoubleGauge != nil {
						kind := metadata.GAUGE
						for _, p := range t.DoubleGauge.DataPoints {
							noticeError(m, visitor(res, m.Name, kind, false, p))
						}
						continue
					}
				case *otlpmetrics.Metric_IntSum:
					if t.IntSum != nil {
						kind := toModelKind(t.IntSum.AggregationTemporality)
						for _, p := range t.IntSum.DataPoints {
							noticeError(m, visitor(res, m.Name, kind, t.IntSum.IsMonotonic, p))
						}
						continue
					}
				case *otlpmetrics.Metric_DoubleSum:
					if t.DoubleSum != nil {
						kind := toModelKind(t.DoubleSum.AggregationTemporality)
						for _, p := range t.DoubleSum.DataPoints {
							noticeError(m, visitor(res, m.Name, kind, t.DoubleSum.IsMonotonic, p))
						}
						continue
					}
				case *otlpmetrics.Metric_IntHistogram:
					if t.IntHistogram != nil {
						kind := toModelKind(t.IntHistogram.AggregationTemporality)
						for _, p := range t.IntHistogram.DataPoints {
							noticeError(m, visitor(res, m.Name, kind, false, p))
						}
						continue
					}
				case *otlpmetrics.Metric_DoubleHistogram:
					if t.DoubleHistogram != nil {
						kind := toModelKind(t.DoubleHistogram.AggregationTemporality)
						for _, p := range t.DoubleHistogram.DataPoints {
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
