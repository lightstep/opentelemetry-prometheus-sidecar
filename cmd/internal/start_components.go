package internal

import (
	"context"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/metadata"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/otlp"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/prometheus"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/retrieval"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/tail"
	"github.com/oklog/run"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/tsdb/wal"
)

type SidecarConfig struct {
	ClientFactory otlp.StorageClientFactory
	Monitor       *prometheus.Monitor
	Logger        log.Logger
	InstanceId    string
	Filters       [][]*labels.Matcher
	MetricRenames map[string]string
	MetadataCache *metadata.Cache

	config.MainConfig
}

// createPrimaryDestinationResourceLabels returns the OTLP resources
// to use for the primary destination.
func createPrimaryDestinationResourceLabels(svcInstanceId string, extraLabels map[string]string) labels.Labels {
	// Note: there is minor benefit in including an external label
	// to indicate the process ID here.  See
	// https://github.com/lightstep/opentelemetry-prometheus-sidecar/issues/44
	// Until resources are serialized once per request, leave this
	// commented out (and a test in e2e_test.go):
	// extraLabels[externalLabelPrefix+string(semconv.ServiceInstanceIDKey)]
	// = svcInstanceId
	return labels.FromMap(extraLabels)
}

func StartComponents(ctx context.Context, scfg SidecarConfig, tailer *tail.Tailer, startOffset int) (int, error) {
	// Run two inter-dependent components:
	// (1) Prometheus reader
	// (2) Queue manager
	// TODO: Replace this with x/sync/errgroup
	currentSegment := 0
	queueManager, err := otlp.NewQueueManager(
		log.With(scfg.Logger, "component", "queue_manager"),
		scfg.QueueConfig(),
		scfg.Destination.Timeout.Duration,
		scfg.ClientFactory,
		tailer,
		retrieval.LabelsToResource(createPrimaryDestinationResourceLabels(scfg.InstanceId, scfg.Destination.Attributes)),
	)
	if err != nil {
		level.Error(scfg.Logger).Log("msg", "creating queue manager failed", "err", err)
		return currentSegment, err
	}

	prometheusReader := retrieval.NewPrometheusReader(
		log.With(scfg.Logger, "component", "prom_wal"),
		scfg.Prometheus.WAL,
		tailer,
		scfg.Filters,
		scfg.MetricRenames,
		scfg.MetadataCache,
		queueManager,
		scfg.OpenTelemetry.MetricsPrefix,
		scfg.Prometheus.MaxPointAge.Duration,
		scfg.Monitor.GetScrapeConfig(),
	)

	var g run.Group
	{
		g.Add(
			func() error {
				level.Info(scfg.Logger).Log("msg", "starting Prometheus reader", "segment", startOffset/wal.DefaultSegmentSize)
				return prometheusReader.Run(ctx, startOffset)
			},
			func(err error) {
				// Prometheus reader needs to be stopped before closing the TSDB
				// so that it doesn't try to write samples to a closed storage.
				// See the use of `stopCh` below to explain how this works.
				level.Info(scfg.Logger).Log("msg", "stopping Prometheus reader")
			},
		)
	}
	{
		stopCh := make(chan struct{})
		g.Add(
			func() error {
				if err := queueManager.Start(); err != nil {
					return err
				}
				level.Info(scfg.Logger).Log("msg", "starting OpenTelemetry writer")
				<-stopCh
				return nil
			},
			func(err error) {
				if err := queueManager.Stop(); err != nil {
					level.Error(scfg.Logger).Log(
						"msg", "stopping OpenTelemetry writer",
						"err", err,
					)
				}
				close(stopCh)
			},
		)
	}

	if err := g.Run(); err != nil {
		level.Error(scfg.Logger).Log("msg", "run loop error", "err", err)
		return prometheusReader.CurrentSegment(), err
	}
	return prometheusReader.CurrentSegment(), nil
}
