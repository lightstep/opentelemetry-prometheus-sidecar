package internal

import (
	"context"
	"net"
	"net/url"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"go.opentelemetry.io/otel/semconv"
)

type ShutdownFunc func(context.Context)

func StartTelemetry(scfg SidecarConfig, defaultSvcName string, isSuper bool) *telemetry.Telemetry {
	diagConfig := scfg.Diagnostics

	if scfg.DisableDiagnostics {
		return telemetry.InternalOnly()
	}

	if diagConfig.Endpoint == "" {
		diagConfig = scfg.Destination
	}

	if diagConfig.Endpoint == "" {
		return telemetry.InternalOnly()
	}

	// reportingPeriod should be faster than the health check period,
	// because we are using metrics data for internal health checking.
	reportingPeriod := scfg.Admin.HealthCheckPeriod.Duration / 2

	return startTelemetry(diagConfig, reportingPeriod, defaultSvcName, scfg.InstanceId, isSuper, scfg.Logger)
}

func startTelemetry(diagConfig config.OTLPConfig, reportingPeriod time.Duration, defaultSvcName string, svcInstanceId string, isSuper bool, logger log.Logger) *telemetry.Telemetry {
	endpoint, _ := url.Parse(diagConfig.Endpoint)
	hostport := endpoint.Hostname()
	if len(endpoint.Port()) > 0 {
		hostport = net.JoinHostPort(hostport, endpoint.Port())
	}

	insecure := endpoint.Scheme == "http"
	metricsHostport := hostport
	spanHostport := hostport

	svcName := diagConfig.Attributes[string(semconv.ServiceNameKey)]
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

	diagConfig.Attributes[string(semconv.ServiceNameKey)] = svcName
	diagConfig.Attributes[string(semconv.ServiceInstanceIDKey)] = svcInstanceId
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
		telemetry.WithMetricReportingPeriod(reportingPeriod),
		telemetry.WithCompressor(diagConfig.Compression),
	)
}
