package sidecar

import (
	"github.com/prometheus/common/version"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

const (
	// Data exported from Prometheus are recored as:
	ExportInstrumentationLibrary = "prometheus-sidecar"

	// Diagnostics about this process are recorded as:
	SelfInstrumentationLibrary = "github.com/lightstep/opentelemetry-prometheus-sidecar"
)

var (
	OTelMeter = otel.Meter(
		SelfInstrumentationLibrary,
		metric.WithInstrumentationVersion(version.Version),
	)

	OTelMeterMust = metric.Must(OTelMeter)
)
