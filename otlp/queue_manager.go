// Copyright 2013 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package otlp

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	sidecar "github.com/lightstep/opentelemetry-prometheus-sidecar"
	metricsService "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/collector/metrics/v1"
	metric_pb "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/metrics/v1"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/tail"
	"github.com/pkg/errors"
	"github.com/prometheus/prometheus/config"
	"go.opentelemetry.io/otel/label"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/time/rate"

	// gRPC Status protobuf types we may want to see.  This type
	// is not widely used, but is the most standard way to itemize
	// validation errors in response to a gRPC request.  If the
	// service happens to be doing this and the user is not
	// careful, they'll miss these details, so we explicitly print
	// them in this code base to avoid potential confusion.
	_ "google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// We track samples in/out and how long pushes take using an Exponentially
	// Weighted Moving Average.
	ewmaWeight          = 0.2
	shardUpdateDuration = 15 * time.Second

	// Limit to 1 log event every 10s
	logRateLimit = 0.1
	logBurst     = 10

	maxErrorDetailStringLen = 512

	queueName = label.Key("queue")
)

// StorageClient defines an interface for sending a batch of samples to an
// external timeseries database.
type StorageClient interface {
	// Store stores the given metric families in the remote storage.
	Store(*metricsService.ExportMetricsServiceRequest) error
	// Test this connection
	Selftest(context.Context) error
	// Release the resources allocated by the client.
	Close() error
}

type StorageClientFactory interface {
	New() StorageClient
	Name() string
}

// QueueManager manages a queue of samples to be sent to the Storage
// indicated by the provided StorageClient.
type QueueManager struct {
	logger log.Logger

	cfg           config.QueueConfig
	clientFactory StorageClientFactory
	queueName     string
	logLimiter    *rate.Limiter

	shardsMtx   sync.RWMutex
	shards      *shardCollection
	numShards   int
	reshardChan chan int
	quit        chan struct{}
	wg          sync.WaitGroup

	samplesIn, samplesOut, samplesOutDuration *ewmaRate
	walSize, walOffset                        *ewmaRate

	tailer               *tail.Tailer
	lastSize, lastOffset int

	succeededSamplesTotal metric.Int64Counter
	failedSamplesTotal    metric.Int64Counter
	sentBatchDuration     metric.Float64ValueRecorder
	queueLengthCounter    metric.Int64UpDownCounter
	queueCapacityObs      metric.Int64SumObserver
	numShardsObs          metric.Int64UpDownSumObserver
}

// NewQueueManager builds a new QueueManager.
func NewQueueManager(logger log.Logger, cfg config.QueueConfig, clientFactory StorageClientFactory, tailer *tail.Tailer) (*QueueManager, error) {
	if logger == nil {
		logger = log.NewNopLogger()
	}
	t := &QueueManager{
		logger:        logger,
		cfg:           cfg,
		clientFactory: clientFactory,
		queueName:     clientFactory.Name(),

		logLimiter:  rate.NewLimiter(logRateLimit, logBurst),
		numShards:   1,
		reshardChan: make(chan int),
		quit:        make(chan struct{}),

		samplesIn:          newEWMARate(ewmaWeight, shardUpdateDuration),
		samplesOut:         newEWMARate(ewmaWeight, shardUpdateDuration),
		samplesOutDuration: newEWMARate(ewmaWeight, shardUpdateDuration),
		walSize:            newEWMARate(ewmaWeight, shardUpdateDuration),
		walOffset:          newEWMARate(ewmaWeight, shardUpdateDuration),
		tailer:             tailer,
	}
	lastSize, err := tailer.Size()
	if err != nil {
		return nil, errors.Wrap(err, "get WAL size")
	}
	t.lastSize = lastSize
	t.lastOffset = tailer.Offset()

	t.shards = t.newShardCollection(t.numShards)

	t.succeededSamplesTotal = sidecar.OTelMeterMust.NewInt64Counter(
		"export.samples.success",
		metric.WithDescription(
			"Total number of samples successfully sent to remote storage.",
		),
	)
	t.failedSamplesTotal = sidecar.OTelMeterMust.NewInt64Counter(
		"export.samples.failed",
		metric.WithDescription(
			"Total number of samples which failed on send to remote storage.",
		),
	)
	t.sentBatchDuration = sidecar.OTelMeterMust.NewFloat64ValueRecorder(
		"export.samples.duration",
		metric.WithDescription(
			"Duration of sample batch send calls to the remote storage.",
		),
	)
	t.queueLengthCounter = sidecar.OTelMeterMust.NewInt64UpDownCounter(
		"export.queue.size",
		metric.WithDescription(
			"The number of processed samples queued to be sent to the remote storage.",
		),
	)
	t.queueCapacityObs = sidecar.OTelMeterMust.NewInt64SumObserver(
		"export.queue.limit",
		func(ctx context.Context, result metric.Int64ObserverResult) {
			result.Observe(int64(t.cfg.Capacity))
		},
		metric.WithDescription(
			"The capacity of the queue of samples to be sent to the remote storage.",
		),
	)
	t.numShardsObs = sidecar.OTelMeterMust.NewInt64UpDownSumObserver(
		"export.queue.shards",
		func(ctx context.Context, result metric.Int64ObserverResult) {
			result.Observe(int64(t.numShards))
		},
		metric.WithDescription(
			"The number of shards used for parallel sending to the remote storage.",
		),
	)

	return t, nil
}

// Append queues a sample to be sent to the OpenTelemetry API.
// Always returns nil.
func (t *QueueManager) Append(hash uint64, sample *metric_pb.ResourceMetrics) error {
	t.queueLengthCounter.Add(context.Background(), 1, queueName.String(t.queueName))

	t.shardsMtx.RLock()
	t.shards.enqueue(hash, sample)
	t.shardsMtx.RUnlock()
	return nil
}

// Start the queue manager sending samples to the remote storage.
// Does not block.
func (t *QueueManager) Start() error {
	t.wg.Add(2)
	go t.updateShardsLoop()
	go t.reshardLoop()

	t.shardsMtx.Lock()
	defer t.shardsMtx.Unlock()
	t.shards.start()

	return nil
}

// Stop stops sending samples to the remote storage and waits for pending
// sends to complete.
func (t *QueueManager) Stop() error {
	level.Info(t.logger).Log("msg", "Stopping remote storage...")
	close(t.quit)
	t.wg.Wait()

	t.shardsMtx.Lock()
	defer t.shardsMtx.Unlock()
	t.shards.stop()

	level.Info(t.logger).Log("msg", "Remote storage stopped.")
	return nil
}

func (t *QueueManager) updateShardsLoop() {
	defer t.wg.Done()

	ticker := time.NewTicker(shardUpdateDuration)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			t.calculateDesiredShards()
		case <-t.quit:
			return
		}
	}
}

func (t *QueueManager) calculateDesiredShards() {
	// Get current wal size and offset but don't return on failure so we can
	// always call tick() for all rates below.
	wsz, err := t.tailer.Size()
	if err != nil {
		level.Error(t.logger).Log("msg", "get WAL size", "err", err)
	}
	woff := t.tailer.Offset()

	t.walSize.incr(int64(wsz - t.lastSize))
	t.walOffset.incr(int64(woff - t.lastOffset))

	// The ewma rates are intialized with a specific interval at which we have to guarantee that
	// tick is called for each.
	// Since the current function is called every interval, this is the point where we do this
	// for all rates at once. This ensures they are sensical to use for comparisons and computations
	// with each other.
	t.samplesIn.tick()
	t.samplesOut.tick()
	t.samplesOutDuration.tick()
	t.walSize.tick()
	t.walOffset.tick()

	if err != nil {
		return
	}

	var (
		sizeRate           = t.walSize.rate()
		offsetRate         = t.walOffset.rate()
		samplesIn          = t.samplesIn.rate()
		samplesOut         = t.samplesOut.rate()
		samplesOutDuration = t.samplesOutDuration.rate()
	)
	t.lastSize = wsz
	t.lastOffset = woff

	if samplesOut == 0 {
		return
	}
	// We compute desired amount of shards based on the time required to delivered a sample.
	// We multiply by a weight of 1.5 to overprovision our number of shards. This ensures
	// that if we can send more samples, the picked shard count has capacity for them.
	// This ensures that we have a feedback loop that keeps growing shards on subsequent
	// calculations until further increase does not increase the throughput anymore.
	timePerSample := samplesOutDuration / samplesOut
	desiredShards := (timePerSample / float64(time.Second)) * 1.5 * samplesIn

	// If the WAL grows faster than we can process it, we are about to build up a backlog.
	// We increase the shards proportionally to get the processing and growth rate to the same level.
	// If we are processing the WAL faster than it grows, we are already working down a backlog
	// and increase throughput as well.
	if sizeRate >= offsetRate {
		desiredShards *= sizeRate / offsetRate
	} else {
		desiredShards *= 1 + (1-(sizeRate/offsetRate))*1.5
	}

	level.Debug(t.logger).Log("msg", "QueueManager.calculateDesiredShards", "samplesIn", samplesIn,
		"samplesOut", samplesOut, "samplesOutDuration", samplesOutDuration, "timePerSample", timePerSample,
		"sizeRate", sizeRate, "offsetRate", offsetRate, "desiredShards", desiredShards)

	// Only change number of shards if the change up or down is significant enough
	// to justifty the caused disruption.
	// We are more eager to increase the number of shards than to decrease it.
	var (
		lowerBound = float64(t.numShards) * 0.7
		upperBound = float64(t.numShards) * 1.1
	)
	level.Debug(t.logger).Log("msg", "QueueManager.updateShardsLoop",
		"lowerBound", lowerBound, "desiredShards", desiredShards, "upperBound", upperBound)
	if lowerBound <= desiredShards && desiredShards <= upperBound {
		return
	}

	numShards := int(math.Ceil(desiredShards))
	if numShards > t.cfg.MaxShards {
		numShards = t.cfg.MaxShards
	} else if numShards < 1 {
		numShards = 1
	}
	if numShards == t.numShards {
		return
	}

	// Resharding can take some time, and we want this loop
	// to stay close to shardUpdateDuration.
	select {
	case t.reshardChan <- numShards:
		level.Debug(t.logger).Log("msg", "Remote storage resharding", "from", t.numShards, "to", numShards)
		t.numShards = numShards
	default:
		level.Debug(t.logger).Log("msg", "Currently resharding, skipping", "to", numShards)
	}
}

func (t *QueueManager) reshardLoop() {
	defer t.wg.Done()

	for {
		select {
		case numShards := <-t.reshardChan:
			t.reshard(numShards)
		case <-t.quit:
			return
		}
	}
}

func (t *QueueManager) reshard(n int) {
	t.shardsMtx.Lock()
	newShards := t.newShardCollection(n)
	oldShards := t.shards
	t.shards = newShards
	oldShards.stop()
	t.shardsMtx.Unlock()

	// We start the newShards after we have stopped (the therefore completely
	// flushed) the oldShards, to guarantee we only every deliver samples in
	// order.
	newShards.start()
}

type queueEntry struct {
	sample *metric_pb.ResourceMetrics
}

type shard struct {
	queue chan queueEntry
}

func newShard(cfg config.QueueConfig) shard {
	return shard{
		queue: make(chan queueEntry, cfg.Capacity),
	}
}

type shardCollection struct {
	qm     *QueueManager
	shards []shard
	done   chan struct{}
	wg     sync.WaitGroup
}

func (t *QueueManager) newShardCollection(numShards int) *shardCollection {
	shards := make([]shard, numShards)
	for i := 0; i < numShards; i++ {
		shards[i] = newShard(t.cfg)
	}
	s := &shardCollection{
		qm:     t,
		shards: shards,
		done:   make(chan struct{}),
	}
	s.wg.Add(numShards)
	return s
}

func (s *shardCollection) start() {
	for i := range s.shards {
		go s.runShard(i)
	}
}

func (s *shardCollection) stop() {
	for _, shard := range s.shards {
		close(shard.queue)
	}
	s.wg.Wait()
	level.Debug(s.qm.logger).Log("msg", "Stopped resharding")
}

func (s *shardCollection) enqueue(hash uint64, sample *metric_pb.ResourceMetrics) {
	s.qm.samplesIn.incr(1)
	shardIndex := hash % uint64(len(s.shards))
	s.shards[shardIndex].queue <- queueEntry{sample: sample}
}

func (s *shardCollection) runShard(i int) {
	defer s.wg.Done()
	client := s.qm.clientFactory.New()
	defer client.Close()
	shard := s.shards[i]

	// Send batches of at most MaxSamplesPerSend samples to the remote storage.
	// If we have fewer samples than that, flush them out after a deadline
	// anyways.
	pendingSamples := make([]*metric_pb.ResourceMetrics, 0, s.qm.cfg.MaxSamplesPerSend)

	timer := time.NewTimer(time.Duration(s.qm.cfg.BatchSendDeadline))
	stop := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	defer stop()

	for {
		select {
		case entry, ok := <-shard.queue:
			sample := entry.sample

			if !ok {
				if len(pendingSamples) > 0 {
					s.sendSamples(client, pendingSamples)
				}
				return
			}
			s.qm.queueLengthCounter.Add(context.Background(), -1, queueName.String(s.qm.queueName))

			pendingSamples = append(pendingSamples, sample)
			if len(pendingSamples) >= s.qm.cfg.MaxSamplesPerSend {
				s.sendSamples(client, pendingSamples)
				pendingSamples = pendingSamples[:0]

				stop()
				timer.Reset(time.Duration(s.qm.cfg.BatchSendDeadline))
			}
		case <-timer.C:
			if len(pendingSamples) > 0 {
				s.sendSamples(client, pendingSamples)
				pendingSamples = pendingSamples[:0]
			}
			timer.Reset(time.Duration(s.qm.cfg.BatchSendDeadline))
		}
	}
}

func (s *shardCollection) sendSamples(client StorageClient, samples []*metric_pb.ResourceMetrics) {
	begin := time.Now()
	s.sendSamplesWithBackoff(client, samples)

	// These counters are used to calculate the dynamic sharding, and as such
	// should be maintained irrespective of success or failure.
	s.qm.samplesOut.incr(int64(len(samples)))
	s.qm.samplesOutDuration.incr(int64(time.Since(begin)))
}

// sendSamples to the remote storage with backoff for recoverable errors.
func (s *shardCollection) sendSamplesWithBackoff(client StorageClient, samples []*metric_pb.ResourceMetrics) {
	ctx := context.Background()

	backoff := s.qm.cfg.MinBackoff
	for {
		begin := time.Now()
		err := client.Store(&metricsService.ExportMetricsServiceRequest{ResourceMetrics: samples})

		s.qm.sentBatchDuration.Record(
			ctx,
			time.Since(begin).Seconds(),
			queueName.String(s.qm.queueName),
		)
		if err == nil {
			s.qm.succeededSamplesTotal.Add(
				ctx,
				int64(len(samples)),
				queueName.String(s.qm.queueName),
			)
			return
		}

		if !isRecoverable(err) {
			level.Warn(s.qm.logger).Log(
				"msg", "unrecoverable write error",
				"err", truncateErrorString(err),
			)
			break
		}
		time.Sleep(time.Duration(backoff))
		backoff = backoff * 2
		if backoff > s.qm.cfg.MaxBackoff {
			backoff = s.qm.cfg.MaxBackoff
		}
	}

	s.qm.failedSamplesTotal.Add(
		ctx,
		int64(len(samples)),
		queueName.String(s.qm.queueName),
	)
}

func isRecoverable(err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	status, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch status.Code() {
	case codes.DeadlineExceeded, codes.Canceled, codes.ResourceExhausted,
		codes.Aborted, codes.OutOfRange, codes.Unavailable, codes.DataLoss:
		// See https://github.com/open-telemetry/opentelemetry-specification/
		// blob/master/specification/protocol/otlp.md#response
		return true
	default:
		return false
	}
}

// truncateErrorString avoids printing error messages that are very
// large.
func truncateErrorString(err error) string {
	tmp := fmt.Sprint(err)
	if len(tmp) > maxErrorDetailStringLen {
		tmp = fmt.Sprint(tmp[:maxErrorDetailStringLen], " ...")
	}
	return tmp
}
