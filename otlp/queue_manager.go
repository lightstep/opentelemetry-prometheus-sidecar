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
	"unsafe"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	sidecar "github.com/lightstep/opentelemetry-prometheus-sidecar"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/retrieval"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry/doevery"
	"github.com/pkg/errors"
	"github.com/prometheus/common/version"
	promconfig "github.com/prometheus/prometheus/config"
	"go.opentelemetry.io/otel/metric"
	metricsService "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

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

	maxErrorDetailStringLen = 512
)

var (
	staticInstrumentationLibrary = &commonpb.InstrumentationLibrary{
		Name:    sidecar.ExportInstrumentationLibrary,
		Version: version.Version,
	}
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

type LogReaderClient interface {
	Size() (int, error)
	Offset() int
}

// QueueManager manages a queue of samples to be sent to the Storage
// indicated by the provided StorageClient.
type QueueManager struct {
	logger log.Logger

	cfg           promconfig.QueueConfig
	timeout       time.Duration
	clientFactory StorageClientFactory
	resource      *resourcepb.Resource

	// shardsMtx is used during Start(), Stop(), and reshard()
	shardsMtx sync.RWMutex

	// shards is a set of running shards.
	shards shardMap

	// queue receives sized and counted Metric batches from the
	// reader.  this channel is not closed.
	queue chan retrieval.SizedMetric

	// numShards is updated by updateShardsLoop, current value
	// passed to the reshard loop.
	numShards int

	// reshardChan carries the output of updateShardsLoop to the
	// reshard loop.  reshard() is synchronous, changes to
	// numShards are ignored if reshard takes longer than an
	// update interval.
	reshardChan chan int

	// quit sends a signal to the resharding loop and updater to exit
	quit chan struct{}

	// wg is used to ensure the resharding loop and updater exit before
	// Stop() returns.
	wg sync.WaitGroup

	// stopped and started ensure that Start and Stop are not
	// called out of order.  The current run.Group code usage in
	// start_components.go does not prevent Stop() being called
	// before Start().
	stopped, started bool

	samplesIn, samplesOut, samplesOutDuration *ewmaRate
	walSize, walOffset                        *ewmaRate

	tailer               LogReaderClient
	lastSize, lastOffset int

	sendOutcomesCounter metric.Int64Counter
	queueLengthCounter  metric.Int64UpDownCounter
	queueRunningCounter metric.Int64UpDownCounter
	queueCapacityObs    metric.Int64UpDownSumObserver
	numShardsObs        metric.Int64UpDownSumObserver
	walSizeObs          metric.Int64UpDownSumObserver
	walOffsetObs        metric.Int64UpDownSumObserver
}

// NewQueueManager builds a new QueueManager.
func NewQueueManager(logger log.Logger, cfg promconfig.QueueConfig, timeout time.Duration, clientFactory StorageClientFactory, tailer LogReaderClient, resource *resourcepb.Resource) (*QueueManager, error) {
	if logger == nil {
		logger = log.NewNopLogger()
	}
	if timeout == 0 {
		timeout = config.DefaultExportTimeout
	}
	t := &QueueManager{
		logger:        logger,
		cfg:           cfg, // TODO: Move cfg into MainConfig
		timeout:       timeout,
		clientFactory: clientFactory,

		numShards:   cfg.MinShards,
		queue:       make(chan retrieval.SizedMetric, cfg.Capacity),
		reshardChan: make(chan int),
		quit:        make(chan struct{}),

		samplesIn:          newEWMARate(ewmaWeight, shardUpdateDuration),
		samplesOut:         newEWMARate(ewmaWeight, shardUpdateDuration),
		samplesOutDuration: newEWMARate(ewmaWeight, shardUpdateDuration),
		walSize:            newEWMARate(ewmaWeight, shardUpdateDuration),
		walOffset:          newEWMARate(ewmaWeight, shardUpdateDuration),
		tailer:             tailer,
		resource:           resource,
	}
	lastSize, err := tailer.Size()
	if err != nil {
		return nil, errors.Wrap(err, "get WAL size")
	}
	t.lastSize = lastSize
	t.lastOffset = tailer.Offset()

	t.sendOutcomesCounter = sidecar.OTelMeterMust.NewInt64Counter(
		config.OutcomeMetric,
		metric.WithDescription(
			"The number of processed samples queued to be sent to the remote storage.",
		),
	)
	t.queueLengthCounter = sidecar.OTelMeterMust.NewInt64UpDownCounter(
		"sidecar.queue.size",
		metric.WithDescription(
			"The number of processed samples queued to be sent to the remote storage.",
		),
	)
	t.queueRunningCounter = sidecar.OTelMeterMust.NewInt64UpDownCounter(
		"sidecar.queue.running",
		metric.WithDescription(
			"The number of shards that have started and not stopped.",
		),
	)
	t.queueCapacityObs = sidecar.OTelMeterMust.NewInt64UpDownSumObserver(
		"sidecar.queue.capacity",
		func(ctx context.Context, result metric.Int64ObserverResult) {
			result.Observe(int64(t.cfg.Capacity))
		},
		metric.WithDescription(
			"The capacity of the queue of samples to be sent to the remote storage.",
		),
	)
	t.numShardsObs = sidecar.OTelMeterMust.NewInt64UpDownSumObserver(
		"sidecar.queue.shards",
		func(ctx context.Context, result metric.Int64ObserverResult) {
			t.shardsMtx.Lock()
			defer t.shardsMtx.Unlock()
			result.Observe(int64(t.numShards))
		},
		metric.WithDescription(
			"The number of shards used for parallel sending to the remote storage.",
		),
	)
	t.walSizeObs = sidecar.OTelMeterMust.NewInt64UpDownSumObserver(
		"sidecar.wal.size",
		func(ctx context.Context, result metric.Int64ObserverResult) {
			result.Observe(int64(t.lastSize))
		},
	)
	t.walOffsetObs = sidecar.OTelMeterMust.NewInt64UpDownSumObserver(
		"sidecar.wal.offset",
		func(ctx context.Context, result metric.Int64ObserverResult) {
			result.Observe(int64(t.lastOffset))
		},
	)

	if unsafe.Sizeof(int(0)) != 8 {
		return nil, fmt.Errorf("this code requires 64bit ints")
	}

	return t, nil
}

// Append queues a sample to be sent to the OpenTelemetry API.
func (t *QueueManager) Append(sample retrieval.SizedMetric) {
	t.queueLengthCounter.Add(context.Background(), int64(sample.Count()))
	t.samplesIn.incr(int64(sample.Count()))

	t.queue <- sample
}

// Start the queue manager sending samples to the remote storage.
// Does not block.
func (t *QueueManager) Start() error {
	t.shardsMtx.Lock()
	defer t.shardsMtx.Unlock()

	if t.started {
		return errors.New("already started")
	}
	if t.stopped {
		return errors.New("already stopped")
	}
	t.started = true

	t.wg.Add(2)
	t.shards = map[*shard]struct{}{}
	go t.updateShardsLoop()
	go t.reshardLoop()

	for i := 0; i < t.numShards; i++ {
		t.startShard()
	}

	return nil
}

// numShardsRunning is for testing
func (t *QueueManager) numShardsRunning() int {
	t.shardsMtx.Lock()
	defer t.shardsMtx.Unlock()
	return len(t.shards)
}

// Stop stops sending samples to the remote storage and waits for pending
// sends to complete.
func (t *QueueManager) Stop() error {
	toStop, err := func() (shardMap, error) {
		t.shardsMtx.Lock()
		defer t.shardsMtx.Unlock()

		if !t.started {
			return nil, errors.New("not started")
		}
		if t.stopped {
			return nil, errors.New("already stopped")
		}

		t.stopped = true

		toStop := t.shards
		t.shards = nil

		// Note: Do not close t.queue, as it means handling more cases
		// in runShard().

		close(t.quit)

		return toStop, nil
	}()

	if err == nil {
		t.wg.Wait()
	}

	for sh := range toStop {
		t.stopShard(sh)
	}

	return err
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
	if numShards < t.cfg.MinShards {
		numShards = t.cfg.MinShards
	} else if numShards > t.cfg.MaxShards {
		numShards = t.cfg.MaxShards
	}
	if numShards < 1 { // Sanitize if needed.
		numShards = 1
	}

	// Note: we do not block here, so that this period stays close
	// to shardUpdateDuration.
	select {
	case t.reshardChan <- numShards:
		if numShards != t.numShards {
			level.Info(t.logger).Log(
				"msg", "send queue resharding",
				"from", t.numShards,
				"to", numShards,
			)
			t.numShards = numShards
		}
	default:
		level.Warn(t.logger).Log(
			"msg", "currently resharding, skipping",
			"to", numShards,
		)
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

// reshard changes the number of shards in `t.shards`.  It does not
// adjust `t.numShards`.  This blocks until the shard number change
// is complete, but does not wait for stopped shards to exit.
func (t *QueueManager) reshard(n int) {
	if n < 1 {
		// Do not remove all running shards!
		return
	}

	toStop := func() (toStop []*shard) {
		t.shardsMtx.Lock()
		defer t.shardsMtx.Unlock()

		for len(t.shards) < n {
			t.startShard()
		}
		for len(t.shards) > n {
			for sh := range t.shards {
				delete(t.shards, sh)
				toStop = append(toStop, sh)
				break
			}
		}
		return toStop
	}()

	for _, sh := range toStop {
		t.stopShard(sh)
	}
}

func (t *QueueManager) stopShard(sh *shard) {
	close(sh.stop)
	sh.wait.Wait()
}

type shard struct {
	stop chan struct{}
	wait sync.WaitGroup
}

type shardMap map[*shard]struct{}

func (t *QueueManager) startShard() {
	sh := &shard{
		stop: make(chan struct{}),
	}
	t.shards[sh] = struct{}{}
	sh.wait.Add(1)
	go t.runShard(sh)
}

func (t *QueueManager) runShard(sh *shard) {
	defer sh.wait.Done()

	client := t.clientFactory.New()
	defer client.Close()

	var pendingSamples []*metricspb.Metric
	var pendingCount int
	var pendingSize int

	ctx := context.Background()

	t.queueRunningCounter.Add(ctx, 1)
	defer t.queueRunningCounter.Add(ctx, -1)

	timer := time.NewTimer(time.Duration(t.cfg.BatchSendDeadline))
	stopTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	defer stopTimer()

	for {
		select {
		case <-sh.stop:
			// This shard is stopping.  Flush and return.
			if len(pendingSamples) > 0 {
				t.sendSamples(client, pendingSamples, pendingCount)
			}
			return

		case entry, _ := <-t.queue:
			// Note: t.queue is never closed.

			// Remove count from the queue size metric.
			t.queueLengthCounter.Add(ctx, int64(-entry.Count()))

			pendingSamples = append(pendingSamples, entry.Metric())
			pendingSize += entry.Size()
			pendingCount += entry.Count()

			// Note: t.cfg.MaxSamplesPerSend is a byte
			// size being passed in a field we no longer
			// care for, see the TODO in config.go about
			// removing the use of promconfig.QueueConfig.
			if pendingSize >= t.cfg.MaxSamplesPerSend {
				// Send a batch.
				t.sendSamples(client, pendingSamples, pendingCount)
				pendingSamples = pendingSamples[:0]
				pendingSize = 0
				pendingCount = 0

				stopTimer()
				timer.Reset(time.Duration(t.cfg.BatchSendDeadline))
			}
		case <-timer.C:
			if len(pendingSamples) > 0 {
				t.sendSamples(client, pendingSamples, pendingCount)
				pendingSamples = pendingSamples[:0]
				pendingSize = 0
				pendingCount = 0
			}
			timer.Reset(time.Duration(t.cfg.BatchSendDeadline))
		}
	}
}

func (t *QueueManager) sendSamples(client StorageClient, samples []*metricspb.Metric, count int) {
	req := &metricsService.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{
			{
				Resource: t.resource,
				InstrumentationLibraryMetrics: []*metricspb.InstrumentationLibraryMetrics{
					{
						InstrumentationLibrary: staticInstrumentationLibrary,
						Metrics:                samples,
					},
				},
			},
		},
	}

	begin := time.Now()
	t.sendSamplesWithBackoff(client, req, count)

	// These counters are used to calculate the dynamic sharding, and as such
	// should be maintained irrespective of success or failure.
	t.samplesOut.incr(int64(count))
	t.samplesOutDuration.incr(int64(time.Since(begin)))
}

// sendSamples to the remote storage with backoff for recoverable errors.
func (t *QueueManager) sendSamplesWithBackoff(client StorageClient, req *metricsService.ExportMetricsServiceRequest, count int) {
	ctx := context.Background()
	start := time.Now()
	backoff := t.cfg.MinBackoff

	// maxWait is provided as a backstop for points that might
	// never succeed, e.g., because they take so long as to
	// repeatedly time out.
	maxWait := t.timeout * time.Duration(config.DefaultMaxExportAttempts)

	for time.Since(start) < maxWait {
		err := client.Store(req)

		if err == nil {
			t.sendOutcomesCounter.Add(
				ctx,
				int64(count),
				config.OutcomeKey.String(config.OutcomeSuccessValue))
			return
		}

		if !isRecoverable(err) {
			t.sendOutcomesCounter.Add(
				ctx,
				int64(count),
				config.OutcomeKey.String("failed"))

			level.Error(t.logger).Log(
				"msg", "unrecoverable write error",
				"points", count,
				"err", truncateErrorString(err),
			)
			return
		}

		t.sendOutcomesCounter.Add(
			ctx,
			int64(count),
			config.OutcomeKey.String("retry"))

		doevery.TimePeriod(config.DefaultNoisyLogPeriod, func() {
			level.Warn(t.logger).Log(
				"msg", "recoverable write error",
				"points", count,
				"err", truncateErrorString(err),
			)
		})

		time.Sleep(time.Duration(backoff))
		backoff = backoff * 2
		if backoff > t.cfg.MaxBackoff {
			backoff = t.cfg.MaxBackoff
		}
	}

	t.sendOutcomesCounter.Add(
		ctx,
		int64(count),
		config.OutcomeKey.String("aborted"))

	level.Error(t.logger).Log(
		"msg", "aborted write",
		"points", count,
	)
	return
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
