package retrieval

import (
	"context"
	"github.com/go-kit/kit/log"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/leader"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

type settableLeader struct {
	leader atomic.Value
}

func (s settableLeader) Start(_ context.Context) error {
	return nil
}

func (s settableLeader) IsLeader() bool {
	return s.leader.Load().(bool)
}

var _ leader.Candidate = (*settableLeader)(nil)

func TestDelayWhenLeaderShouldEndWithoutContextError(t *testing.T) {
	l := &settableLeader{}
	l.leader.Store(true)
	logger := log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))

	d := delay{
		duration:      30 * time.Second,
		intervalCheck: 5 * time.Second,
		lc:            l,
		logger:        logger,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	d.delayNonLeaderSidecar(ctx, 0)
	if ctx.Err() != nil {
		t.Error("context should not contain error when sidecar is leader")
	}
}

func TestDelayWhenNotLeaderShouldEndWithContextError(t *testing.T) {
	l := &settableLeader{}
	l.leader.Store(false)
	logger := log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))

	d := delay{
		duration:      time.Minute,
		intervalCheck: 10 * time.Millisecond,
		lc:            l,
		logger:        logger,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	d.delayNonLeaderSidecar(ctx, time.Now().UnixNano()/int64(time.Millisecond))
	if ctx.Err() == nil {
		t.Error("context should timeout when not leader")
	}
}

func TestDelayWhenNotLeaderShouldDelay(t *testing.T) {
	l := &settableLeader{}
	l.leader.Store(false)
	logger := log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))

	d := delay{
		duration:      150 * time.Millisecond,
		intervalCheck: 10 * time.Millisecond,
		lc:            l,
		logger:        logger,
	}

	ctx := context.Background()

	before := time.Now()
	d.delayNonLeaderSidecar(ctx, time.Now().UnixNano()/int64(time.Millisecond))

	if delayTime := time.Since(before); delayTime < 150*time.Millisecond {
		t.Errorf("expected at least 150ms of delay, got %s instead", delayTime)
	}
}

func TestDelayBecomeLeaderShouldStopDelay(t *testing.T) {
	l := &settableLeader{}
	l.leader.Store(false)
	logger := log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))

	d := delay{
		duration:      5 * time.Minute,
		intervalCheck: 10 * time.Millisecond,
		lc:            l,
		logger:        logger,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	go func() {
		time.Sleep(100 * time.Millisecond)
		l.leader.Store(true)
	}()

	d.delayNonLeaderSidecar(ctx, time.Now().UnixNano()/int64(time.Millisecond))
	if ctx.Err() != nil {
		t.Errorf("context should not contain error when sidecar becomes leader before maximum delay time, got error: %v", ctx.Err())
	}

}
