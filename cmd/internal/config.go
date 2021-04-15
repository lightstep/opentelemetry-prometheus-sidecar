package internal

import (
	"github.com/go-kit/kit/log"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/common"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/metadata"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/otlp"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/prometheus"
	"github.com/prometheus/prometheus/pkg/labels"
)

type SidecarConfig struct {
	ClientFactory otlp.StorageClientFactory
	Monitor       *prometheus.Monitor
	Logger        log.Logger

	// InstanceId is a unique identifer for this process.
	InstanceId    string
	Matchers      [][]*labels.Matcher
	MetricRenames map[string]string
	MetadataCache *metadata.Cache

	FailingReporter common.FailingReporter

	config.MainConfig
}
