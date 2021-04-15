package internal

import (
	"context"
	"strings"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/otlp"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/retrieval"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/tail"
	"github.com/oklog/run"
	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/tsdb/wal"
)

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

func NewTailer(ctx context.Context, scfg SidecarConfig) (*tail.Tailer, error) {
	return tail.Tail(
		ctx,
		log.With(scfg.Logger, "component", "wal_reader"),
		scfg.Prometheus.WAL,
		scfg.Monitor,
	)
}

func StartComponents(ctx context.Context, scfg SidecarConfig, tailer tail.WalTailer, startOffset int) error {
	var err error
	attempts := 0
	currentSegment := 0
	for {
		currentSegment, err = runComponents(ctx, scfg, tailer, startOffset)
		if err != nil && attempts < config.DefaultMaxRetrySkipSegments && strings.Contains(err.Error(), tail.ErrSkipSegment.Error()) {
			_ = tailer.Close()
			_ = retrieval.SaveProgressFile(scfg.Prometheus.WAL, startOffset)
			tailer, err = NewTailer(ctx, scfg)
			if err != nil {
				_ = level.Error(scfg.Logger).Log("msg", "tailing WAL failed", "err", err)
				break
			}
			attempts += 1
			// SetCurrentSegment is being called here to ensure that the tailer
			// is able to continue past truncated logs after initialization
			// this addresses the case where a segment is detected as truncated
			// and its reason for being truncated is *not* a checkpoint happening
			tailer.SetCurrentSegment(currentSegment)
			startOffset = currentSegment * wal.DefaultSegmentSize
			continue
		}
		break
	}
	return err
}

func runComponents(ctx context.Context, scfg SidecarConfig, tailer tail.WalTailer, startOffset int) (int, error) {
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
		_ = level.Error(scfg.Logger).Log("msg", "creating queue manager failed", "err", err)
		return currentSegment, err
	}

	prometheusReader := retrieval.NewPrometheusReader(
		log.With(scfg.Logger, "component", "prom_wal"),
		scfg.Prometheus.WAL,
		tailer,
		scfg.Matchers,
		scfg.MetricRenames,
		scfg.MetadataCache,
		queueManager,
		scfg.OpenTelemetry.MetricsPrefix,
		scfg.Prometheus.MaxPointAge.Duration,
		scfg.Monitor.GetScrapeConfig(),
		scfg.FailingReporter,
	)

	var g run.Group
	{
		g.Add(
			func() error {
				_ = level.Info(scfg.Logger).Log("msg", "starting Prometheus reader", "segment", startOffset/wal.DefaultSegmentSize)
				return prometheusReader.Run(ctx, startOffset)
			},
			func(err error) {
				// Prometheus reader needs to be stopped before closing the TSDB
				// so that it doesn't try to write samples to a closed storage.
				// See the use of `stopCh` below to explain how this works.
				_ = level.Info(scfg.Logger).Log("msg", "stopping Prometheus reader")
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
				_ = level.Info(scfg.Logger).Log("msg", "starting OpenTelemetry writer")
				<-stopCh
				return nil
			},
			func(err error) {
				if err := queueManager.Stop(); err != nil {
					_ = level.Error(scfg.Logger).Log(
						"msg", "stopping OpenTelemetry writer",
						"err", err,
					)
				}
				close(stopCh)
			},
		)
	}

	if err := g.Run(); err != nil {
		_ = level.Error(scfg.Logger).Log("msg", "run loop error", "err", err)
		return prometheusReader.CurrentSegment(), err
	}
	return prometheusReader.CurrentSegment(), nil
}
