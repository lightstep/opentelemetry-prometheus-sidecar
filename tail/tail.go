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
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
	"github.com/prometheus/prometheus/tsdb/record"
	"github.com/prometheus/prometheus/tsdb/wal"
)

// Tailer tails a write ahead log in a given directory.
type Tailer struct {
	ctx context.Context
	dir string
	cur io.ReadCloser

	logger log.Logger

	mtx         sync.Mutex
	nextSegment int
	offset      int // Bytes read within the current reader.
}

// Tail the prometheus/tsdb write ahead log in the given directory. Checkpoints
// are read before reading any WAL segments.
// Tailing may fail if we are racing with the DB itself in deleting obsolete checkpoints
// and segments. The caller should implement relevant logic to retry in those cases.
func Tail(ctx context.Context, logger log.Logger, dir string) (*Tailer, error) {
	t := &Tailer{
		ctx:    ctx,
		dir:    dir,
		logger: logger,
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

const segmentSize = 128 * 1024 * 1024

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
	return last.index*segmentSize + int(fi.Size()), nil
}

func (t *Tailer) incOffset(v int) {
	t.mtx.Lock()
	t.offset += v
	t.mtx.Unlock()
}

func (t *Tailer) incNextSegment() {
	t.mtx.Lock()
	t.nextSegment++
	t.offset = 0
	t.mtx.Unlock()
}

func (t *Tailer) getNextSegment() int {
	t.mtx.Lock()
	v := t.nextSegment
	t.mtx.Unlock()
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
	return (t.nextSegment-1)*segmentSize + t.offset
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
		if err == record.ErrNotFound {
			// Next segment doesn't exist yet. We'll probably just have to
			// wait for more data to be written.  Note: We may also be
			// in a race with Prometheus cleaning its WAL.
			level.Warn(t.logger).Log(
				"msg", "waiting for write-ahead log segment",
				"segment", segment,
			)

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
		t.cur = next
		t.incNextSegment()
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
