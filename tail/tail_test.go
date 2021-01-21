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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/prometheus/tsdb/wal"
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

func TestTailFuzz(t *testing.T) {
	dir, err := ioutil.TempDir("", "test_tail")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithCancel(context.Background())

	rc, err := Tail(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	var wg sync.WaitGroup
	wg.Add(1)

	w, err := wal.NewSize(nil, nil, dir, 2*1024*1024, false)
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
		wg.Done()
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

func BenchmarkTailFuzz(t *testing.B) {
	dir, err := ioutil.TempDir("", "test_tail")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rc, err := Tail(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()

	w, err := wal.NewSize(nil, nil, dir, 32*1024*1024, false)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	t.SetBytes(4 * 2000) // Average record size times worker count.
	t.ResetTimer()

	var rec [4000]byte
	count := t.N * 4
	for k := 0; k < 4; k++ {
		go func() {
			for i := 0; i < count/4; i++ {
				if err := w.Log(rec[:rand.Intn(4000)]); err != nil {
					panic(err)
				}
			}
		}()
	}

	wr := wal.NewReader(rc)

	for i := 1; wr.Next(); i++ {
		if i == t.N*4 {
			break
		}
	}
}
