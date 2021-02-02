package internal

import (
	"github.com/lightstep/opentelemetry-prometheus-sidecar/otlp"
)

type otlpClientFactory otlp.ClientConfig

var _ otlp.StorageClientFactory = otlpClientFactory{}

func NewOTLPClientFactory(cc otlp.ClientConfig) otlp.StorageClientFactory {
	return otlpClientFactory(cc)
}

func (s otlpClientFactory) New() otlp.StorageClient {
	return otlp.NewClient(otlp.ClientConfig(s))
}

func (s otlpClientFactory) Name() string {
	return s.URL.String()
}
