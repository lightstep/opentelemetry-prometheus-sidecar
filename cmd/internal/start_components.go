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

// externalLabelPrefix is a non-standard convention for indicating
// external labels in the Prometheus data model, which are not
// semantically defined in OTel, as recognized by Lightstep.
const externalLabelPrefix = "__external_"

// createPrimaryDestinationResourceLabels returns the OTLP resources
// to use for the primary destination.
func createPrimaryDestinationResourceLabels(svcInstanceId string, externalLabels labels.Labels, extraLabels map[string]string) labels.Labels {
	// TODO: Enable and test the following line, as https://github.com/lightstep/opentelemetry-prometheus-sidecar/issues/44
	// has been merged.
	// extraLabels[externalLabelPrefix+string(semconv.ServiceInstanceIDKey)]
	// = svcInstanceId

	allLabels := make(map[string]string)
	for _, label := range externalLabels {
		allLabels[externalLabelPrefix+label.Name] = label.Value
	}
	for name, value := range extraLabels {
		allLabels[name] = value
	}

	return labels.FromMap(allLabels)
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
			// The following check is to ensure that if a sidecar error'd on
			// a truncated segment and the truncation was *not* due to a checkpoint,
			// that truncated segment is skipped when the reader is restart. Otherwise
			// the reader will reset nextSegment to the next segment after the
			// checkpoint and hit the same truncated file. NOTE: if the truncation
			// was caused by a checkpoint, we shouldn't do anything and let the
			// reader continue on.
			//
			// NOTE: this case *should* never happen
			if currentSegment > tailer.CurrentSegment() {
				_ = level.Warn(scfg.Logger).Log("msg", "unexpected segment truncation", "currentSegment", err, "tailer.CurrentSegment", tailer.CurrentSegment())
				tailer.SetNextSegment(currentSegment + 1)
				startOffset = currentSegment * wal.DefaultSegmentSize
			}

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
		retrieval.LabelsToResource(createPrimaryDestinationResourceLabels(
			scfg.InstanceId,
			scfg.Monitor.GetGlobalConfig().ExternalLabels,
			scfg.Destination.Attributes)),
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
		scfg.LeaderCandidate,
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
				_ = level.Info(scfg.Logger).Log("msg", "stopping OpenTelemetry writer")
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
