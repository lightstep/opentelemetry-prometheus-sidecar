package internal

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/prometheus/prometheus/pkg/labels"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
)

type ShutdownFunc func(context.Context)

func StartTelemetry(scfg SidecarConfig, defaultSvcName string, isSuper bool, externalLabels labels.Labels) *telemetry.Telemetry {
	diagConfig := scfg.Diagnostics

	if scfg.DisableDiagnostics {
		return telemetry.InternalOnly()
	}

	if diagConfig.Endpoint == "" {
		// Create a copy, as we adjust the headers/attributes.
		diagConfig = scfg.Destination.Copy()
	}

	if diagConfig.Endpoint == "" {
		return telemetry.InternalOnly()
	}

	// reportingPeriod should be faster than the health check period,
	// because we are using metrics data for internal health checking.
	reportingPeriod := scfg.Admin.HealthCheckPeriod.Duration / 2

	return startTelemetry(diagConfig, reportingPeriod, defaultSvcName, scfg.InstanceId, isSuper, externalLabels, scfg.Logger)
}

func startTelemetry(diagConfig config.OTLPConfig, reportingPeriod time.Duration, defaultSvcName string, svcInstanceId string, isSuper bool, externalLabels labels.Labels, logger log.Logger) *telemetry.Telemetry {
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

	diagConfig.Headers[config.AgentKey] = agentName
	diagConfig.Attributes[string(semconv.ServiceNameKey)] = svcName
	diagConfig.Attributes[string(semconv.ServiceInstanceIDKey)] = svcInstanceId

	// No need to add an external-label-prefix for the secondary target.
	for _, label := range externalLabels {
		diagConfig.Attributes[label.Name] = label.Value
	}

	// No need to log this for the supervisor case.
	if !isSuper {
		level.Info(logger).Log(
			"msg", "configuring sidecar diagnostics",
			"attributes", fmt.Sprintf("%s", diagConfig.Attributes),
		)
	}

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
