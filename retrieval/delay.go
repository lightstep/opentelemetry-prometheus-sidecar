package retrieval

import (
	"context"
	"github.com/go-kit/kit/log"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/leader"
	"time"
)

type delay struct {
	duration      time.Duration
	intervalCheck time.Duration
	lc            leader.Candidate
	logger        log.Logger
}

// delayNonLeaderSidecar delays non leader sidecars to create a
// gap cover when a new leader is elected.
//
// This will delay the tailer for a maximum of delay.duration.
// After delay.intervalCheck a check to see if the sidecar has
// become the leader is done, if the sidecar is leading
// we resume the tailer otherwise we continue delaying up to
// the delay.duration.
//
//
// This is a safe measure to reduce the chances of creating gaps
// from the time that a sidecar steps down and another sidecar
// becomes the leader.
func (d *delay) delayNonLeaderSidecar(ctx context.Context, millis int64) {
	if d.lc.IsLeader() {
		return
	}

	ts := int64(time.Duration(millis) * time.Millisecond / time.Nanosecond)
	timestamp := time.Unix(0, ts)
	if time.Since(timestamp) > d.duration {
		return
	}

	d.logger.Log("msg", "delaying tailer", "delay_time", d.duration)
	for i := 0; i < int(d.duration/d.intervalCheck); i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.intervalCheck):
			if d.lc.IsLeader() {
				d.logger.Log("msg", "resuming tailer, sidecar is now leading")
				return
			}
		}
	}
}
