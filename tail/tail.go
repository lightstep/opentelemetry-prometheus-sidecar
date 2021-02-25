// Copyright 2018 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tail

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	sidecar "github.com/lightstep/opentelemetry-prometheus-sidecar"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/prometheus"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry/doevery"
	"github.com/pkg/errors"
	"github.com/prometheus/prometheus/tsdb/record"
	"github.com/prometheus/prometheus/tsdb/wal"
	"go.opentelemetry.io/otel/metric"
)

const (
	// promPageSize is the standard Prometheus page size.
	// Segment transitions should take place at multiples of this
	// size, or else something is wrong.
	promPageSize = 32 * 1024

	// promSegmentSize is the Prometheus segment size.  Although
	// the Prom code makes this a variable, the application does
	// not (apparently?) expose it as a variable, so we hard-code.
	promSegmentSize = wal.DefaultSegmentSize
)

var (
	segmentOpenCounter = sidecar.OTelMeterMust.NewInt64Counter(
		"sidecar.segment.opens",
		metric.WithDescription(
			"The number of attempts to open a WAL segment",
		),
	)
	segmentReadCounter = sidecar.OTelMeterMust.NewInt64Counter(
		"sidecar.segment.reads",
		metric.WithDescription(
			"The number of attempts to read a WAL segment",
		),
	)
	segmentByteCounter = sidecar.OTelMeterMust.NewInt64Counter(
		"sidecar.segment.bytes",
		metric.WithDescription(
			"The number of bytes read from WAL segments",
		),
	)
	segmentErrorCounter = sidecar.OTelMeterMust.NewInt64Counter(
		"sidecar.segment.errors",
		metric.WithDescription(
			"The number of invalid segments errors encountered",
		),
	)

	ErrRestartReader = errors.New("sidecar fell behind, restarting reader")
)

// Tailer tails a write ahead log in a given directory.
type Tailer struct {
	ctx context.Context
	dir string
	cur io.ReadCloser

	logger  log.Logger
	promURL *url.URL
	monitor *prometheus.Monitor

	mtx         sync.Mutex
	nextSegment int
	offset      int // Bytes read within the current reader.
}

// Tail the prometheus/tsdb write ahead log in the given directory. Checkpoints
// are read before reading any WAL segments.
// Tailing may fail if we are racing with the DB itself in deleting obsolete checkpoints
// and segments. The caller should implement relevant logic to retry in those cases.
func Tail(ctx context.Context, logger log.Logger, dir string, promURL *url.URL) (*Tailer, error) {
	mu := *promURL
	mu.Path = path.Join(mu.Path, "metrics")
	t := &Tailer{
		ctx:     ctx,
		dir:     dir,
		logger:  logger,
		promURL: promURL,
		monitor: prometheus.NewMonitor(&mu),
	}
	cpdir, k, err := wal.LastCheckpoint(dir)
	if errors.Cause(err) == record.ErrNotFound {
		// TODO: Test this code path, where the sidecar starts before
		// Prometheus ever begins recording a WAL.  This can lead to
		// an indefinite wait if misconfigured.
		t.cur = ioutil.NopCloser(bytes.NewReader(nil))
		t.nextSegment = 0
	} else if err != nil {
		return nil, errors.Wrap(err, "retrieve last checkpoint")
	} else {
		// Open the entire checkpoint first. It has to be consumed before
		// the tailer proceeds to any segments.  During this initial
		// segment the segment number equals the checkpoint and the
		// offset relates to the concatenation of checkpoint segments.
		t.cur, err = wal.NewSegmentsReader(cpdir)
		if err != nil {
			return nil, errors.Wrap(err, "open checkpoint")
		}
		// We will resume reading ordinary segments at k+1.
		t.nextSegment = k + 1
	}
	return t, nil
}

type segmentRef struct {
	name  string
	index int
}

// listSegments was last copied from Prometheus v2.22.0.  TODO: keep
// this up to date.
func listSegments(dir string) (refs []segmentRef, err error) {
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		fn := f.Name()
		k, err := strconv.Atoi(fn)
		if err != nil {
			continue
		}
		refs = append(refs, segmentRef{name: fn, index: k})
	}
	sort.Slice(refs, func(i, j int) bool {
		return refs[i].index < refs[j].index
	})
	for i := 0; i < len(refs)-1; i++ {
		if refs[i].index+1 != refs[i+1].index {
			return nil, errors.New("segments are not sequential")
		}
	}
	return refs, nil
}

// Size returns the total size of the WAL as indicated by its highest segment.
// It includes the size of any past segments that may no longer exist.
func (t *Tailer) Size() (int, error) {
	segs, err := listSegments(t.dir)
	if err != nil || len(segs) == 0 {
		return 0, err
	}
	last := segs[len(segs)-1]

	fi, err := os.Stat(filepath.Join(t.dir, last.name))
	if err != nil {
		return 0, err
	}
	return last.index*promSegmentSize + int(fi.Size()), nil
}

func (t *Tailer) incOffset(v int) {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	t.offset += v
}

func (t *Tailer) currentOffset() int {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	return t.offset
}

func (t *Tailer) incNextSegment() int {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	finalOffset := t.offset
	t.nextSegment++
	t.offset = 0
	return finalOffset
}

func (t *Tailer) getNextSegment() int {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	return t.nextSegment
}

func (t *Tailer) getCurrentSegment() int {
	t.mtx.Lock()
	defer t.mtx.Unlock()
	return t.nextSegment - 1
}

// Offset returns the approximate current position of the tailer in the WAL with
// respect to Size().
func (t *Tailer) Offset() int {
	t.mtx.Lock()
	defer t.mtx.Unlock()

	// Handle tailer that was initialized against an empty WAL.
	if t.nextSegment == 0 {
		return 0
	}
	return (t.nextSegment-1)*promSegmentSize + t.offset
}

// Close all underlying resources of the tailer.
func (t *Tailer) Close() error {
	return t.cur.Close()
}

// CurrentSegment returns the index of the currently read segment.
// If no successful read has been performed yet, it may be negative.
func (t *Tailer) CurrentSegment() int {
	return t.getNextSegment() - 1
}

func (t *Tailer) waitForReadiness() error {
	// Note: no timeout on the context, we're really waiting.
	return prometheus.WaitForReady(t.ctx, t.logger, t.promURL)
}

func (t *Tailer) getPrometheusSegment() (int, error) {
	ctx, cancel := context.WithTimeout(t.ctx, config.DefaultHealthCheckTimeout)
	defer cancel()

	res, err := t.monitor.Get(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "prometheus /metrics")
	}

	seg := int(res.Gauge(config.PrometheusCurrentSegmentMetricName).For(nil))

	if seg > 0 {
		return seg, nil
	}

	// Prometheus does not set this metric until it advances to a new
	// segment.  If it's segment 0, we will not see the metric yet.
	srs, err := listSegments(t.dir)
	if err == nil && len(srs) == 1 && srs[0].index == 0 {
		return 0, nil
	}

	return 0, errors.New("cannot determine current segment from /metrics")
}

func (t *Tailer) Read(b []byte) (int, error) {
	// When we read until EOF, we'll check in with Prometheus this often.
	const maxBackoff = config.DefaultHealthCheckTimeout
	backoff := 100 * time.Millisecond

	sleepContextDone := func() bool {
		select {
		case <-time.After(backoff):
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
			return false
		case <-t.ctx.Done():
			return true
		}
	}

	for {
		// Any Read result other than io.EOF is simply returned.  If any
		// data was read, return it before considering the EOF cases.
		n, err := t.cur.Read(b)
		segmentReadCounter.Add(t.ctx, 1)
		segmentByteCounter.Add(t.ctx, int64(n))

		if err != io.EOF {
			t.incOffset(n)
			return n, err
		} else if n != 0 {
			t.incOffset(n)
			return n, nil
		}

		// EOF cases where no data was read follow.

		// Note: Prometheus DOES NOT fsync the old segment before opening
		// the new one.  There is a theoretical race between opening the
		// new segment and finishing the old one.  However, in most cases
		// if we see a partial block and EOF, it means that Prometheus
		// did not shut down cleanly, in which case we wait for restart
		// to finish the block.

		select {
		case <-t.ctx.Done():
			// Indicates SIGTERM, return EOF. This will make the WAL
			// reader identify a corruption if we terminate mid-
			// stream. But at least we have a clean shutdown if we
			// realy read until the end of a stopped WAL.
			return 0, io.EOF
		default:
		}

		// When we are NOT on a blocksize-aligned boundary, we're going
		// to wait, whether Prometheus is alive or not.  If it's alive,
		// this means we're keeping up with the WAL.  If it's dead, this
		// means we're going to wait.  When Prometheus starts and writes
		// its next checkpoint, this block will be filled with zeros.
		currentOffset := t.currentOffset()
		blockSizeAligned := currentOffset&(promPageSize-1) == 0

		// Sleep, then and check for readiness.  These two steps return
		// only when context is done (SIGTERM, i.e., clean shutdown).
		if sleepContextDone() {
			return 0, io.EOF
		}
		// Sleeping even when block aligned ensures we do not slam
		// Prometheus with readiness checks and metrics scrapes.  It
		// should not be needed when block aligned, assuming lots of
		// other correctness.
		if err := t.waitForReadiness(); err != nil {
			return 0, err
		}

		promSeg, err := t.getPrometheusSegment()
		if err != nil {
			// We can't get the current segment despite determining
			// that Prometheus was ready.  It may be unhealthy,
			// CPU starved, or restarting.  We'll wait.
			doevery.TimePeriod(config.DefaultNoisyLogPeriod, func() {
				level.Warn(t.logger).Log(
					"msg", "scraping for current WAL segment",
					"err", err,
					"wal_contents", fmt.Sprint(dirContents(t.dir)),
				)
			})
			continue
		}

		currentSegment := t.getCurrentSegment()
		nextSegment := currentSegment + 1

		if promSeg == currentSegment {
			// We slept, Prometheus says it's still writing this
			// segment, now try again.  (If block size aligned,
			// possibly the server is shutting down.)
			doevery.TimePeriod(config.DefaultNoisyLogPeriod, func() {
				level.Debug(t.logger).Log(
					"msg", "WAL reader is up-to-date",
					"segment", currentSegment,
					"offset", currentOffset,
				)
			})
			continue
		}

		// Prometheus is apparently writing a new segment.  If we saw an
		// incomplete block, this could be the theoretical race condition
		// with fsync stated above.
		if !blockSizeAligned && promSeg == nextSegment {
			// Note: If this happens, we should be near to the end of
			// a segment.  This should only repeat when the fsync is
			// taking a long time.  If this becomes a serious problem
			// there is a second metric we could test to monitor
			// fsync completion.
			doevery.TimePeriod(config.DefaultNoisyLogPeriod, func() {
				level.Info(t.logger).Log(
					"msg", "WAL reader waiting for fsync",
					"segment", currentSegment,
					"offset", currentOffset,
				)
			})
			continue
		}

		if !blockSizeAligned {
			// If the promSeg is more than 1 ahead of the reader but
			// the block size is not aligned, we have a serious
			// inconsistency.
			level.Error(t.logger).Log(
				"msg", "truncated WAL segment",
				"segment", currentSegment,
				"offset", currentOffset,
			)
			segmentErrorCounter.Add(t.ctx, 1)
			return 0, ErrRestartReader
		}

		if promSeg < currentSegment {
			doevery.TimePeriod(config.DefaultNoisyLogPeriod, func() {
				level.Error(t.logger).Log(
					"msg", "WAL segment in the future",
					"prometheus_current", promSeg,
					"sidecar_current", currentSegment,
				)
			})
			return 0, errors.Errorf(
				"WAL segment in the future %d < %d",
				promSeg,
				currentSegment,
			)
		}

		// Block size was aligned, so either clean shutdown or
		// end-of-segment reached.  Imaginary cases: stuck fsync queue
		// means lots of unflushed data in the WAL segment, despite being
		// block aligned.  Seems unlikely, would lead to unclean shutdown
		// with a unexpected non-zero padding message from the higher
		// level code.

		segmentOpenCounter.Add(t.ctx, 1)
		next, err := openSegment(t.dir, nextSegment)

		if err == record.ErrNotFound && promSeg > nextSegment {
			level.Warn(t.logger).Log(
				"msg", "past WAL segment not found, will restart the reader",
				"segment", nextSegment,
				"current", promSeg,
			)
			return 0, ErrRestartReader
		}

		if err != nil {
			return 0, errors.Wrap(err, "open next segment")
		}

		t.incNextSegment()
		level.Info(t.logger).Log(
			"msg", "transition to WAL segment",
			"segment", t.CurrentSegment(),
		)
		t.cur = next
	}
}

// openSegment finds a WAL segment with a name that parses to n. This
// way we do not need to know how wide the segment filename is (i.e.,
// how many zeros to pad with).
func openSegment(dir string, n int) (io.ReadCloser, error) {
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range files {
		k, err := strconv.Atoi(entry.Name())
		if err != nil || k != n {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		return wal.OpenReadSegment(path)
	}
	return nil, record.ErrNotFound
}

func dirContents(dir string) []string {
	var r []string
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, entry := range files {
		r = append(r, entry.Name())
	}
	return r
}
