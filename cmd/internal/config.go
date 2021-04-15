package internal

import (
	"github.com/go-kit/kit/log"
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
	InstanceId    string
	Matchers      [][]*labels.Matcher
	MetricRenames map[string]string
	MetadataCache *metadata.Cache

	config.MainConfig
}
