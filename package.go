package sidecar

import (
	"github.com/prometheus/common/version"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/global"
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
		otel.WithInstrumentationVersion(version.Version),
	)

	OTelMeterMust = otel.Must(OTelMeter)
)
