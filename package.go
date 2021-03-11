package sidecar

import (
	"github.com/prometheus/common/version"
	"go.opentelemetry.io/otel/metric"
	metricotel "go.opentelemetry.io/otel/metric/global"
)

const (
	// Data exported from Prometheus are recored as:
	ExportInstrumentationLibrary = "prometheus-sidecar"

	// Diagnostics about this process are recorded as:
	SelfInstrumentationLibrary = "github.com/lightstep/opentelemetry-prometheus-sidecar"
)

var (
	OTelMeter = metricotel.Meter(
		SelfInstrumentationLibrary,
		metric.WithInstrumentationVersion(version.Version),
	)

	OTelMeterMust = metric.Must(OTelMeter)
)
