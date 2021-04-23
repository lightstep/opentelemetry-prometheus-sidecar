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
	"io/ioutil"
	"math/rand"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/internal/promtest"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/prometheus"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/pkg/errors"
	"github.com/prometheus/prometheus/tsdb/record"
	"github.com/prometheus/prometheus/tsdb/wal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenSegment(t *testing.T) {
	dir, err := ioutil.TempDir("", "test_open_segment")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	require.NoError(t, ioutil.WriteFile(path.Join(dir, "000000000000000000000nonsense"), []byte("bad"), 0777))

	for i := 0; i < 10; i++ {
		require.NoError(t, ioutil.WriteFile(path.Join(dir, fmt.Sprint("000000000000000000000", i)), []byte(fmt.Sprint(i)), 0777))
	}
	for i := 19; i >= 10; i-- {
		require.NoError(t, ioutil.WriteFile(path.Join(dir, fmt.Sprint("000000000000000000000", i)), []byte(fmt.Sprint(i)), 0777))
	}

	for i := 0; i < 20; i++ {
		rc, err := openSegment(dir, i)
		require.NoError(t, err)
		body, err := ioutil.ReadAll(rc)
		require.NoError(t, err)
		require.Equal(t, fmt.Sprint(i), string(body))
		require.NoError(t, rc.Close())
	}
}

func TestCorruption(t *testing.T) {
	dir := "./testdata/corruption"
	ctx, cancel := context.WithCancel(context.Background())

	prom := promtest.NewFakePrometheus(promtest.Config{})

	rc, err := Tail(ctx, telemetry.DefaultLogger(), dir, prometheus.NewMonitor(prom.ReadyConfig()))
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	wr := wal.NewReader(rc)
	var read [][]byte
	for len(read) < 1 && wr.Next() {
		read = append(read, append([]byte(nil), wr.Record()...))
	}
	if wr.Err() == nil {
		t.Fatal(errors.New("expected corruption error"))
	}

	assert.Contains(t, wr.Err().Error(), "corruption after")

	go func() {
		time.Sleep(time.Second)
		cancel()
	}()
	if wr.Next() {
		t.Fatal("read unexpected record")
	}
	if wr.Err() == nil {
		t.Fatal(errors.New("expected EOF error"))
	}
	assert.Contains(t, wr.Err().Error(), "unexpected EOF")
}

func TestInvalidSegment(t *testing.T) {
	dir := "./testdata/invalid-segment"
	ctx, cancel := context.WithCancel(context.Background())

	prom := promtest.NewFakePrometheus(promtest.Config{})
	prom.SetSegment(2)

	rc, err := Tail(ctx, telemetry.DefaultLogger(), dir, prometheus.NewMonitor(prom.ReadyConfig()))
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	wr := wal.NewReader(rc)

	var wg sync.WaitGroup
	wg.Add(1)

	var read [][]byte
	for len(read) < 1 && wr.Next() {
		read = append(read, append([]byte(nil), wr.Record()...))
	}

	go func() {
		time.Sleep(time.Second)
		cancel()
	}()
	if wr.Next() {
		t.Fatal("read unexpected record")
	}
	if wr.Err() == nil {
		t.Fatal(errors.New("expected segment transition error"))
	}
	assert.Contains(t, wr.Err().Error(), "read first header byte: skip truncated WAL segment")
}

func TestTailFuzz(t *testing.T) {
	dir, err := ioutil.TempDir("", "test_tail")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithCancel(context.Background())

	prom := promtest.NewFakePrometheus(promtest.Config{})

	rc, err := Tail(ctx, telemetry.DefaultLogger(), dir, prometheus.NewMonitor(prom.ReadyConfig()))
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	const segmentSize = 2 * 1024 * 1024

	w, err := wal.NewSize(nil, nil, dir, segmentSize, false)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var written [][]byte
	var read [][]byte

	// Start background writer.
	const count = 50000
	var asyncErr atomic.Value
	go func() {
		defer wg.Done()
		for i := 0; i < count; i++ {
			if i%100 == 0 {
				time.Sleep(time.Duration(rand.Intn(10 * int(time.Millisecond))))
			}
			rec := make([]byte, rand.Intn(5337))
			if _, err := rand.Read(rec); err != nil {
				asyncErr.Store(err)
				break
			}
			if err := w.Log(rec); err != nil {
				asyncErr.Store(err)
				break
			}

			written = append(written, rec)
		}
	}()

	// Asynchronously udate the segment number in order for the
	// Tail reader to make progress, since we are testing a real
	// WAL with a fake Prometheus.
	stopCh := make(chan struct{})
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				ss, err := listSegments(dir)
				require.NoError(t, err)
				if len(ss) > 0 {
					prom.SetSegment(ss[len(ss)-1].index)
				}
			}
		}
	}()

	wr := wal.NewReader(rc)

	// Expect `count` records; read them all, if possible. The test will
	// time out if fewer records show up.
	for len(read) < count && asyncErr.Load() == nil && wr.Next() {
		read = append(read, append([]byte(nil), wr.Record()...))
	}
	if wr.Err() != nil {
		t.Fatal(wr.Err())
	}
	close(stopCh)
	wg.Wait()
	if err := asyncErr.Load(); err != nil {
		t.Fatal("async error: ", err)
	}
	if len(written) != len(read) {
		t.Fatal("didn't read all records: ", len(written), "!=", len(read))
	}
	for i, r := range read {
		if !bytes.Equal(r, written[i]) {
			t.Fatalf("record %d doesn't match", i)
		}
	}
	// Attempt to read one more record, but expect no more records.
	// Give the reader a chance to run for a while, then cancel its
	// context so the test doesn't time out.
	go func() {
		time.Sleep(time.Second)
		cancel()
	}()
	// It's safe to call Next() again. The last invocation must have returned `true`,
	// or else the comparison between `read` and `written` above will fail and cause
	// the test to end early.
	if wr.Next() {
		t.Fatal("read unexpected record")
	}
	if wr.Err() != nil {
		t.Fatal(wr.Err())
	}
}

func TestSlowFsync(t *testing.T) {
	dir, err := ioutil.TempDir("", "test_tail")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prom := promtest.NewFakePrometheus(promtest.Config{})

	const (
		segmentSize = 2 * 1024 * 1024
		recSize     = 1024 * 1024
	)

	rec := make([]byte, recSize)

	w, err := wal.NewSize(nil, nil, dir, segmentSize, false)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	w.Log(rec)

	rc, err := Tail(ctx, telemetry.DefaultLogger(), dir, prometheus.NewMonitor(prom.ReadyConfig()))
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	wr := wal.NewReader(rc)

	// Read one record.
	require.True(t, wr.Next())
	require.Equal(t, recSize, len(wr.Record()))

	// The next record will start a new segment, but set
	// Prometheus unready first.
	prom.SetReady(false)

	go func() {
		time.Sleep(config.DefaultHealthCheckTimeout)
		prom.SetReady(true)
		w.Log(rec)
		time.Sleep(config.DefaultHealthCheckTimeout)
		prom.SetSegment(1)
	}()

	// Reading this second record has to wait for both readiness
	// and the updated segment.
	require.True(t, wr.Next())
	require.Equal(t, recSize, len(wr.Record()))
}

func TestListCheckpoints(t *testing.T) {
	dir, err := ioutil.TempDir("", "test_tail")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// no checkpoint directory exists
	_, _, err = lastCheckpoint(dir)
	require.Error(t, err)
	require.ErrorIs(t, err, record.ErrNotFound)

	// single exists
	err = os.Mkdir(path.Join(dir, "checkpoint.0001"), os.ModeDir)
	require.NoError(t, err)
	cpdir, _, err := lastCheckpoint(dir)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(cpdir, "0001"), "%s did not end with %s", cpdir, "0001")

	// two valid checkpoint exists
	err = os.Mkdir(path.Join(dir, "checkpoint.0002"), os.ModeDir)
	require.NoError(t, err)
	cpdir, _, err = lastCheckpoint(dir)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(cpdir, "0002"), "%s did not end with %s", cpdir, "0002")

	// two valid checkpoint exists and an old tmp file does as well
	err = os.Mkdir(path.Join(dir, "checkpoint.0000.tmp"), os.ModeDir)
	require.NoError(t, err)
	cpdir, _, err = lastCheckpoint(dir)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(cpdir, "0002"), "%s did not end with %s", cpdir, "0002")

	// two valid checkpoint, an old tmp checkpoint and a newer checkpoint in progress
	err = os.Mkdir(path.Join(dir, "checkpoint.0004.tmp"), os.ModeDir)
	require.NoError(t, err)
	cpdir, _, err = lastCheckpoint(dir)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(cpdir, "0004.tmp"), "%s did not end with %s", cpdir, "0004")

	// add another checkpoint testing creation time doesnt affect this
	err = os.Mkdir(path.Join(dir, "checkpoint.0003.tmp"), os.ModeDir)
	require.NoError(t, err)
	cpdir, _, err = lastCheckpoint(dir)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(cpdir, "0004.tmp"), "%s did not end with %s", cpdir, "0004")
}

func TestCheckpointInProgress(t *testing.T) {
	dir, err := ioutil.TempDir("", "test_tail")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	prom := promtest.NewFakePrometheus(promtest.Config{})

	err = os.Mkdir(path.Join(dir, "checkpoint.0001.tmp"), 0777)
	require.NoError(t, err)

	for i := 0; i < 10; i++ {
		require.NoError(t, ioutil.WriteFile(path.Join(dir, fmt.Sprint("checkpoint.0001.tmp/000000000000000000000", i)), []byte(fmt.Sprint(i)), 0777))
	}

	go func() {
		time.Sleep(500 * time.Millisecond)
		err := os.Rename(path.Join(dir, "checkpoint.0001.tmp"), path.Join(dir, "checkpoint.0001"))
		require.NoError(t, err)
	}()

	_, err = Tail(context.Background(), telemetry.DefaultLogger(), dir, prometheus.NewMonitor(prom.ReadyConfig()))
	require.NoError(t, err)
}
