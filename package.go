package sidecar

import (
	"github.com/prometheus/common/version"
	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/api/metric"
)

const (
	// Data exported from Prometheus are recored as:
	ExportInstrumentationLibrary = "prometheus-sidecar"

	// Diagnostics about this process are recorded as:
	SelfInstrumentationLibrary = "github.com/lightstep/opentelemetry-prometheus-sidecar"
)

var (
	OTelMeter = global.Meter(
		SelfInstrumentationLibrary,
		metric.WithInstrumentationVersion(version.Version),
	)

	OTelMeterMust = metric.Must(OTelMeter)
)
