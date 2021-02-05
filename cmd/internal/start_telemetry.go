package internal

import (
	"context"
	"net"
	"net/url"

	"github.com/go-kit/kit/log"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
)

type ShutdownFunc func(context.Context)

func StartTelemetry(cfg config.MainConfig, defaultSvcNAme string, isSuper bool, logger log.Logger) *telemetry.Telemetry {
	diagConfig := cfg.Diagnostics

	if diagConfig.Endpoint == "" && !cfg.DisableDiagnostics {
		diagConfig = cfg.Destination
	}

	if diagConfig.Endpoint == "" {
		return telemetry.InternalOnly()
	}

	return startTelemetry(diagConfig, defaultSvcNAme, isSuper, logger)
}

func startTelemetry(diagConfig config.OTLPConfig, defaultSvcName string, isSuper bool, logger log.Logger) *telemetry.Telemetry {
	endpoint, _ := url.Parse(diagConfig.Endpoint)
	hostport := endpoint.Hostname()
	if len(endpoint.Port()) > 0 {
		hostport = net.JoinHostPort(hostport, endpoint.Port())
	}

	insecure := endpoint.Scheme == "http"
	metricsHostport := hostport
	spanHostport := hostport

	// Set a service.name resource if none is set.
	const serviceNameKey = "service.name"

	svcName := diagConfig.Attributes[serviceNameKey]
	if svcName == "" {
		svcName = defaultSvcName
	}

	agentName := config.AgentSecondaryValue
	if isSuper {
		// Disable metrics in the supervisor
		metricsHostport = ""
		svcName = svcName + "-supervisor"
		agentName = config.AgentSupervisorValue
	} else {
		// Disable spans in the main process
		spanHostport = ""
	}

	diagConfig.Attributes[serviceNameKey] = svcName
	diagConfig.Headers[config.AgentKey] = agentName

	// TODO: Configure trace batching interval.

	return telemetry.ConfigureOpentelemetry(
		telemetry.WithLogger(logger),
		telemetry.WithSpanExporterEndpoint(spanHostport),
		telemetry.WithSpanExporterInsecure(insecure),
		telemetry.WithMetricsExporterEndpoint(metricsHostport),
		telemetry.WithMetricsExporterInsecure(insecure),
		telemetry.WithHeaders(diagConfig.Headers),
		telemetry.WithResourceAttributes(diagConfig.Attributes),
		telemetry.WithExportTimeout(diagConfig.Timeout.Duration),
		telemetry.WithMetricReportingPeriod(config.DefaultReportingPeriod),
		telemetry.WithCompressor(diagConfig.Compression),
	)
}
