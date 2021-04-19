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
	"io/ioutil"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sidecar "github.com/lightstep/opentelemetry-prometheus-sidecar"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/internal/otlptest"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/internal/promtest"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/prometheus"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/tail"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/prometheus/common/model"
	"github.com/prometheus/common/version"
	"github.com/stretchr/testify/require"
	metricsService "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metric_pb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resource_pb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestStorageClient simulates a storage that can store samples and compares it
// with an expected set.
// All inserted series must be uniquely identified by their metric type string.
type TestStorageClient struct {
	receivedSamples map[string][]TestPoint
	expectedSamples map[string][]TestPoint
	wg              sync.WaitGroup
	mtx             sync.Mutex
	t               *testing.T
	checkUniq       bool
}

type TestPoint struct {
	V float64
	T time.Time
}

func newTestSample(name string, timestamp int64, v float64) *metric_pb.Metric {
	return otlptest.DoubleGauge(
		name, "", "",
		otlptest.DoubleDataPoint(
			otlptest.Labels(),
			time.Unix(0, 0),
			time.Unix(timestamp, 0),
			v,
		),
	)
}

func NewTestStorageClient(t *testing.T, checkUniq bool) *TestStorageClient {
	return &TestStorageClient{
		receivedSamples: map[string][]TestPoint{},
		expectedSamples: map[string][]TestPoint{},
		t:               t,
		checkUniq:       checkUniq,
	}
}

func resourceMetric(m *metric_pb.Metric) *metric_pb.ResourceMetrics {
	return otlptest.ResourceMetrics(
		otlptest.Resource(),
		otlptest.InstrumentationLibraryMetrics(
			otlptest.InstrumentationLibrary(sidecar.ExportInstrumentationLibrary, version.Version),
			m,
		),
	)
}

func (c *TestStorageClient) expectSamples(samples []*metric_pb.Metric) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	ctx := context.Background()
	for _, s := range samples {
		vs := otlptest.VisitorState{}
		vs.Visit(ctx, func(
			resource *resource_pb.Resource,
			metricName string,
			kind config.Kind,
			monotonic bool,
			point interface{},
		) error {
			nanos := point.(*metric_pb.DoubleDataPoint).TimeUnixNano
			value := point.(*metric_pb.DoubleDataPoint).Value
			c.expectedSamples[metricName] = append(c.expectedSamples[metricName], TestPoint{
				T: time.Unix(0, int64(nanos)),
				V: value,
			})
			return nil
		}, resourceMetric(s))
	}
	c.wg.Add(len(samples))
}

func (c *TestStorageClient) waitForExpectedSamples(t *testing.T) {
	c.wg.Wait()

	c.mtx.Lock()
	defer c.mtx.Unlock()
	if len(c.receivedSamples) != len(c.expectedSamples) {
		t.Fatalf("Expected %d metric families, received %d",
			len(c.expectedSamples), len(c.receivedSamples))
	}
	for name, expectedSamples := range c.expectedSamples {
		if !reflect.DeepEqual(expectedSamples, c.receivedSamples[name]) {
			t.Fatalf("%s: Expected %v, got %v", name, expectedSamples, c.receivedSamples[name])
		}
	}
}

func (c *TestStorageClient) resetExpectedSamples() {
	c.receivedSamples = map[string][]TestPoint{}
	c.expectedSamples = map[string][]TestPoint{}
}

func (c *TestStorageClient) Store(req *metricsService.ExportMetricsServiceRequest) error {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	ctx := context.Background()

	recv := 0

	for _, ts := range req.ResourceMetrics {
		vs := otlptest.VisitorState{}
		vs.Visit(ctx, func(
			resource *resource_pb.Resource,
			metricName string,
			kind config.Kind,
			monotonic bool,
			point interface{},
		) error {
			nanos := point.(*metric_pb.DoubleDataPoint).TimeUnixNano
			value := point.(*metric_pb.DoubleDataPoint).Value
			recv++

			c.receivedSamples[metricName] = append(c.receivedSamples[metricName], TestPoint{
				T: time.Unix(0, int64(nanos)),
				V: value,
			})
			return nil
		}, ts)
	}
	if c.checkUniq {
		for i, ts := range req.ResourceMetrics {
			ts.InstrumentationLibraryMetrics[0].Metrics[0].Data = nil
			for j, prev := range req.ResourceMetrics[:i] {
				if reflect.DeepEqual(prev, ts) {
					c.t.Fatalf("found duplicate time series in request: %v: %d != %d", ts, i, j)
				}
			}
		}
	}
	for i := 0; i < recv; i++ {
		c.wg.Done()
	}
	return nil
}

func (t *TestStorageClient) New() StorageClient {
	return t
}

func (t *TestStorageClient) Selftest(context.Context) error {
	return nil
}

func (c *TestStorageClient) Name() string {
	return "teststorageclient"
}

func (c *TestStorageClient) Close() error {
	// Note: This does not wait on `c.wg`.  The caller is expected to waitForExpectedSamples().
	return nil
}

func TestSampleDeliverySimple(t *testing.T) {
	ctx := context.Background()
	dir, err := ioutil.TempDir("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Let's create an even number of send batches so we don't run into the
	// batch timeout case.
	n := 100

	var samples []*metric_pb.Metric
	for i := 0; i < n; i++ {
		samples = append(samples, newTestSample(
			fmt.Sprintf("test_metric_%d", i),
			2234567890000,
			float64(i),
		))
	}

	c := NewTestStorageClient(t, true)
	c.expectSamples(samples)

	mainConfig := config.DefaultMainConfig()
	cfg := mainConfig.QueueConfig()
	cfg.Capacity = n
	cfg.MaxSamplesPerSend = n

	prom := promtest.NewFakePrometheus(promtest.Config{})

	tailer, err := tail.Tail(context.Background(), telemetry.DefaultLogger(), dir, prometheus.NewMonitor(prom.ReadyConfig()))
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewQueueManager(nil, cfg, 0, c, tailer, nil)
	if err != nil {
		t.Fatal(err)
	}

	// These should be received by the client.
	for _, s := range samples {
		m.Append(ctx, s)
	}
	m.Start()
	defer m.Stop()

	c.waitForExpectedSamples(t)
}

func TestSampleDeliveryMultiShard(t *testing.T) {
	ctx := context.Background()
	dir, err := ioutil.TempDir("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	numShards := 10
	n := 5 * numShards

	var samples []*metric_pb.Metric
	for i := 0; i < n; i++ {
		samples = append(samples, newTestSample(
			fmt.Sprintf("test_metric_%d", i),
			2234567890000,
			float64(i),
		))
	}

	c := NewTestStorageClient(t, true)

	mainConfig := config.DefaultMainConfig()
	cfg := mainConfig.QueueConfig()
	// flush after each sample, to avoid blocking the test
	cfg.MaxSamplesPerSend = 1
	cfg.MaxShards = numShards

	prom := promtest.NewFakePrometheus(promtest.Config{})

	tailer, err := tail.Tail(context.Background(), telemetry.DefaultLogger(), dir, prometheus.NewMonitor(prom.ReadyConfig()))
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewQueueManager(nil, cfg, 0, c, tailer, nil)
	if err != nil {
		t.Fatal(err)
	}

	m.Start()
	defer m.Stop()
	m.reshard(numShards) // blocks until resharded

	c.expectSamples(samples)
	// These should be received by the client.
	for _, s := range samples {
		m.Append(ctx, s)
	}

	c.waitForExpectedSamples(t)
}

func TestSampleDeliveryTimeout(t *testing.T) {
	dir, err := ioutil.TempDir("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	mainConfig := config.DefaultMainConfig()

	// Let's send one less sample than batch size, and wait the timeout duration
	n := mainConfig.QueueConfig().MaxSamplesPerSend - 1

	var samples1, samples2 []*metric_pb.Metric
	for i := 0; i < n; i++ {
		samples1 = append(samples1, newTestSample(
			fmt.Sprintf("test_metric_%d", i),
			2234567890000,
			float64(i),
		))
		samples2 = append(samples2, newTestSample(
			fmt.Sprintf("test_metric_%d", i),
			2234567890000+1,
			float64(i),
		))

	}

	c := NewTestStorageClient(t, true)
	cfg := mainConfig.QueueConfig()
	cfg.MaxShards = 1
	cfg.BatchSendDeadline = model.Duration(100 * time.Millisecond)

	prom := promtest.NewFakePrometheus(promtest.Config{})

	tailer, err := tail.Tail(context.Background(), telemetry.DefaultLogger(), dir, prometheus.NewMonitor(prom.ReadyConfig()))
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewQueueManager(nil, cfg, 0, c, tailer, nil)
	if err != nil {
		t.Fatal(err)
	}

	m.Start()
	defer m.Stop()

	ctx := context.Background()

	// Send the samples twice, waiting for the samples in the meantime.
	c.expectSamples(samples1)
	for _, s := range samples1 {
		m.Append(ctx, s)
	}
	c.waitForExpectedSamples(t)

	c.resetExpectedSamples()
	c.expectSamples(samples2)

	for _, s := range samples2 {
		m.Append(ctx, s)
	}
	c.waitForExpectedSamples(t)
}

func TestSampleDeliveryOrder(t *testing.T) {
	dir, err := ioutil.TempDir("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ts := 10
	mainConfig := config.DefaultMainConfig()
	n := mainConfig.QueueConfig().MaxSamplesPerSend * ts

	var samples []*metric_pb.Metric
	for i := 0; i < n; i++ {
		samples = append(samples, newTestSample(
			fmt.Sprintf("test_metric_%d", i%ts),
			1234567890001+int64(i),
			float64(i),
		))
	}

	c := NewTestStorageClient(t, false)
	c.expectSamples(samples)

	prom := promtest.NewFakePrometheus(promtest.Config{})

	tailer, err := tail.Tail(context.Background(), telemetry.DefaultLogger(), dir, prometheus.NewMonitor(prom.ReadyConfig()))
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewQueueManager(nil, mainConfig.QueueConfig(), 0, c, tailer, nil)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	m.Start()
	defer m.Stop()
	// These should be received by the client.
	for _, s := range samples {
		m.Append(ctx, s)
	}

	c.waitForExpectedSamples(t)
}

// TestBlockingStorageClient is a queue_manager StorageClient which will block
// on any calls to Store(), until the `block` channel is closed, at which point
// the `numCalls` property will contain a count of how many times Store() was
// called.
type TestBlockingStorageClient struct {
	numCalls uint64
	block    chan bool
}

func NewTestBlockedStorageClient() *TestBlockingStorageClient {
	return &TestBlockingStorageClient{
		block:    make(chan bool),
		numCalls: 0,
	}
}

func (c *TestBlockingStorageClient) Store(_ *metricsService.ExportMetricsServiceRequest) error {
	atomic.AddUint64(&c.numCalls, 1)
	<-c.block
	return nil
}

func (c *TestBlockingStorageClient) NumCalls() uint64 {
	return atomic.LoadUint64(&c.numCalls)
}

func (c *TestBlockingStorageClient) unlock() {
	close(c.block)
}

func (t *TestBlockingStorageClient) New() StorageClient {
	return t
}

func (t *TestBlockingStorageClient) Selftest(context.Context) error {
	return nil
}

func (c *TestBlockingStorageClient) Name() string {
	return "testblockingstorageclient"
}

func (c *TestBlockingStorageClient) Close() error {
	return nil
}

func (t *QueueManager) queueLen() int {
	t.shardsMtx.Lock()
	defer t.shardsMtx.Unlock()
	queueLength := 0
	for _, shard := range t.shards.shards {
		queueLength += len(shard.queue)
	}
	return queueLength
}

func TestRecoverable(t *testing.T) {
	require.True(t, isRecoverable(context.Canceled))
	require.True(t, isRecoverable(context.DeadlineExceeded))
	require.True(t, isRecoverable(status.Error(codes.Unavailable, "try again later")))
	require.False(t, isRecoverable(status.Error(codes.PermissionDenied, "sorry")))
	require.False(t, isRecoverable(fmt.Errorf("no idea what this is")))
}

func TestSpawnNotMoreThanMaxConcurrentSendsGoroutines(t *testing.T) {
	dir, err := ioutil.TempDir("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Our goal is to fully empty the queue:
	// `MaxSamplesPerSend*Shards` samples should be consumed by the
	// per-shard goroutines, and then another `MaxSamplesPerSend`
	// should be left on the queue.
	mainConfig := config.DefaultMainConfig()
	n := mainConfig.QueueConfig().MaxSamplesPerSend * 2

	var samples []*metric_pb.Metric
	for i := 0; i < n; i++ {
		samples = append(samples, newTestSample(
			fmt.Sprintf("test_metric_%d", i),
			2234567890001,
			float64(i),
		))
	}

	c := NewTestBlockedStorageClient()
	cfg := mainConfig.QueueConfig()
	cfg.MaxShards = 1
	cfg.Capacity = n

	prom := promtest.NewFakePrometheus(promtest.Config{})

	tailer, err := tail.Tail(context.Background(), telemetry.DefaultLogger(), dir, prometheus.NewMonitor(prom.ReadyConfig()))
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewQueueManager(nil, cfg, 0, c, tailer, nil)
	if err != nil {
		t.Fatal(err)
	}

	m.Start()

	defer func() {
		c.unlock()
		m.Stop()
	}()

	ctx := context.Background()

	for _, s := range samples {
		m.Append(ctx, s)
	}

	// Wait until the runShard() loops drain the queue.  If things went right, it
	// should then immediately block in sendSamples(), but, in case of error,
	// it would spawn too many goroutines, and thus we'd see more calls to
	// client.Store()
	//
	// The timed wait is maybe non-ideal, but, in order to verify that we're
	// not spawning too many concurrent goroutines, we have to wait on the
	// Run() loop to consume a specific number of elements from the
	// queue... and it doesn't signal that in any obvious way, except by
	// draining the queue.  We cap the waiting at 1 second -- that should give
	// plenty of time, and keeps the failure fairly quick if we're not draining
	// the queue properly.
	for i := 0; i < 100 && m.queueLen() > 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}

	if m.queueLen() != mainConfig.QueueConfig().MaxSamplesPerSend {
		t.Errorf("Failed to drain QueueManager queue, %d elements left",
			m.queueLen(),
		)
	}

	numCalls := c.NumCalls()
	if numCalls != uint64(1) {
		t.Errorf("Saw %d concurrent sends, expected 1", numCalls)
	}
}
