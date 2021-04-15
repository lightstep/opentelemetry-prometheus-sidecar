package common

import (
	sidecar "github.com/lightstep/opentelemetry-prometheus-sidecar"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var (
	DroppedSeries = sidecar.OTelMeterMust.NewInt64Counter(
		config.DroppedSeriesMetric,
		metric.WithDescription("Number of series that could not be exported"),
	)

	DroppedPoints = sidecar.OTelMeterMust.NewInt64Counter(
		config.DroppedPointsMetric,
		metric.WithDescription("Number of points that could not be exported"),
	)

	SkippedPoints = sidecar.OTelMeterMust.NewInt64Counter(
		config.SkippedPointsMetric,
		metric.WithDescription("Number of points that were bypassed"),
	)
)

const (
	DroppedKeyReason attribute.Key = "key_reason"
	MetricNameKey    attribute.Key = "metric_name"
)
