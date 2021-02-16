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
	"io"
	"io/ioutil"
	"net/url"
	"os"
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
		"sidecar.segment.opened",
		metric.WithDescription(
			"The number of attempts to open a WAL segment",
		),
	)
)

// Tailer tails a write ahead log in a given directory.
type Tailer struct {
	ctx context.Context
	dir string
	cur io.ReadCloser

	logger  log.Logger
	promURL *url.URL

	mtx         sync.Mutex
	nextSegment int
	offset      int // Bytes read within the current reader.
}

// Tail the prometheus/tsdb write ahead log in the given directory. Checkpoints
// are read before reading any WAL segments.
// Tailing may fail if we are racing with the DB itself in deleting obsolete checkpoints
// and segments. The caller should implement relevant logic to retry in those cases.
func Tail(ctx context.Context, logger log.Logger, dir string, promURL *url.URL) (*Tailer, error) {
	t := &Tailer{
		ctx:     ctx,
		dir:     dir,
		logger:  logger,
		promURL: promURL,
	}
	cpdir, k, err := wal.LastCheckpoint(dir)
	if errors.Cause(err) == record.ErrNotFound {
		t.cur = ioutil.NopCloser(bytes.NewReader(nil))
		t.nextSegment = 0
	} else if err != nil {
		return nil, errors.Wrap(err, "retrieve last checkpoint")
	} else {
		// Open the entire checkpoint first. It has to be consumed before
		// the tailer proceeds to any segments.
		t.cur, err = wal.NewSegmentsReader(cpdir)
		if err != nil {
			return nil, errors.Wrap(err, "open checkpoint")
		}
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
	v := t.nextSegment
	return v
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

func (t *Tailer) Read(b []byte) (int, error) {
	const maxBackoff = 3 * time.Second
	backoff := 10 * time.Millisecond

	for {
		n, err := t.cur.Read(b)
		if err != io.EOF {
			t.incOffset(n)
			return n, err
		}
		select {
		case <-t.ctx.Done():
			// We return EOF here. This will make the WAL reader identify a corruption
			// if we terminate mid stream. But at least we have a clean shutdown if we
			// realy read till the end of a stopped WAL.
			t.incOffset(n)
			return n, io.EOF
		default:
		}
		// Check if the next segment already exists. Then the current
		// one is really done.
		// We could do something more sophisticated to save syscalls, but this
		// seems fine for the expected throughput (<5MB/s).
		segment := t.getNextSegment()
		next, err := openSegment(t.dir, segment)
		segmentOpenCounter.Add(t.ctx, 1)
		if err == record.ErrNotFound {
			// Next segment doesn't exist yet. We'll probably just have to
			// wait for more data to be written.  Note: We may also be
			// in a race with Prometheus cleaning its WAL.
			doevery.TimePeriod(config.DefaultNoisyLogPeriod, func() {
				level.Warn(t.logger).Log(
					"msg", "waiting for write-ahead log segment",
					"segment", segment,
				)
			})

			// Note: This condition here may leave us deadlocked if the
			// Prometheus server decided to clean old WAL segments while
			// we are reading them.  Note: a similar mechanism could be
			// used here as the one described below.  If we stall at
			// this point, and instead of waiting for a fixed time or
			// the context to be canceled, wait until we see another WAL
			// segment growing again.  If there's a gap, we should probably
			// reset the stream.

			select {
			case <-time.After(backoff):
			case <-t.ctx.Done():
				return n, io.EOF
			}
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		} else if err != nil {
			t.incOffset(n)
			return n, errors.Wrap(err, "open next segment")
		}

		// Note: Prometheus DOES NOT fsync the old segment
		// before opening the new one.  We have a guessing game.
		currentOffset := t.currentOffset()

		for {
			// If we finished a WAL segment, good!
			if currentOffset == promSegmentSize {
				level.Debug(t.logger).Log(
					"msg", "WAL segment complete",
					"segment", segment-1,
				)
				break
			}

			// Check if Prometheus is still ready, as a
			// stall and to help diagnose problems in this area.
			// Note: The sidecar in general has no mechanism to
			// gracefully deal with Prometheus crashing while the
			// sidecar is healthy.  TODO: consider this situation.
			err := func() error {
				ctx, cancel := context.WithTimeout(
					context.Background(),
					config.DefaultHealthCheckTimeout)
				defer cancel()
				return prometheus.WaitForReady(ctx, t.logger, t.promURL)
			}()

			if err != nil {
				// Note: this blocks until either the context is
				// canceled (e.g., by the SIGTERM handler) or Prometheus
				// succeeds.  This indicates the context was canceled.
				return 0, errors.Wrap(err, "waiting for WAL")
			}

			// There's no harm in trying again. We have no idea yet
			// whether there's an fsync() to wait for, but if this
			// Read() succeeds again we're happy.
			if n, err = t.cur.Read(b); err != io.EOF {
				t.incOffset(n)
				return n, err
			}

			// OK, what now?  There are several known possibilities.  A
			// healthy Prometheus appears to always leave files that are
			// a multiple of the block size.  If the file ends at a block
			// size multiple, we should not expect corruption and the next
			// Read() should begin at offset 0 in the folling WAL segment.
			//
			// However, more auditing of the Prometheus code would be needed
			// to be convinced it's not possible for more blocks to appear
			// in the file at this point.
			//
			// It seems to be worth waiting a while, as a (poor) safety
			// mechanism.  30 seconds?  a minute?  What we want to know
			// is whether Prometheus has begun writing to a new segment.
			// Once we see it writing (or any change in the WAL
			// directory) and wait sufficiently long for fsync() to
			// complete, we may be fairly sure that the current segment
			// is finished.
			//
			// TODO: Wait up to 60 seconds or so, or until we see progress
			// on another WAL segment?  This means calling ReadDir() and
			// checking for changes, I guess.
			if weAreSurelyReadyToProceed() {
				break
			}
		}

		finalOffset := t.incNextSegment()

		// Note: This code has triggered in production in a situation where
		// Prometheus did not detect corruption.
		//
		// if finalOffset&(promPageSize-1) != 0 {
		// 	return n, errors.Errorf(
		// 		"segment %d transition at unexpected offset: %d",
		// 		segment-1,
		// 		finalOffset,
		// 	)
		// }
		//
		// We are seeing these "unexpected" conditions but aren't quite sure
		// why.  Either the Prom has crashed with an incomplete WAL block,
		// or Prom is still busy writing that WAL block.  Either way, it's
		// not safe to transition from one WAL segment to another when not
		// at a block size multiple, but this requires understanding the
		// Prometheus logic.
		//
		// In prometheus/tsdb/wal/wal.go, find the function `NewSegmentBufReader()`.
		// This is the code that Prometheus uses to read the WAL at startup
		// and it is coded to deal specifically with the problem we're facing.
		// See their comment:
		//
		//     We hit EOF; fake out zero padding at the end of short segments, so we
		//     don't increment curr too early and report the wrong segment as corrupt.
		//
		// This code appears to allow Prometheus to recover to health even when
		// there are incomplete blocks.  It does not necesarily imply corruption,
		// as seen in the transcript of
		// https://github.com/lightstep/opentelemetry-prometheus-sidecar/issues/108
		// where Prometheus recovered into a healthy state while sidecar hit
		// corruption.
		//
		// To address this, I see two ways forward:
		// 1. This code can insert zero padding when it detects a non-block-aligned
		//    WAL segment transition.
		// 2. This code can return failures that the reader recovers from, essentially
		//    force it to re-open its reader when something bad happens.
		//
		// The second approach will work in a more general sense, since
		// there are other conditions that may cause us to require the
		// reader to reset.  Ideally, we do not crash in this situation, because
		// we have a warm metadata cache.
		//
		// To be concrete, suppose that Read returns a special kind of error
		// to indicate suspected corruption.  This will propagate up to
		// (*PrometheusReader).Run() which will have to start again.

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
		return wal.OpenReadSegment(filepath.Join(dir, entry.Name()))
	}
	return nil, record.ErrNotFound
}
