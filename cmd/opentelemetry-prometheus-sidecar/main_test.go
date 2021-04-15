// Copyright 2017 The Prometheus Authors
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

package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/internal/promtest"
	"github.com/stretchr/testify/require"
	traces "go.opentelemetry.io/proto/otlp/trace/v1"
)

// Note: the tests would be cleaner in this package if
//   var bout, berr bytes.Buffer
//   cmd.Stdout = &bout
//   cmd.Stderr = &berr
// followed by
//   t.Logf("stdout: %v\n", bout.String())
//   t.Logf("stderr: %v\n", berr.String())
// and assertions was replaced with a helper class.  When the test
// times out unexpectedly, this information is not being printed,
// which the helper could fix with a pipe.

func TestMain(m *testing.M) {
	if os.Getenv("RUN_MAIN") == "" {
		// Run the test directly.
		os.Exit(m.Run())
	}

	main()
}

func (ts *testServer) runPrometheusService(cfg promtest.Config) {
	fp := promtest.NewFakePrometheus(cfg)
	address := "0.0.0.0:19093"
	server := &http.Server{
		Addr:    address,
		Handler: fp.ServeMux(),
	}
	ctx, cancel := context.WithCancel(context.Background())

	ts.stops <- cancel

	go server.ListenAndServe()

	go func() {
		<-ctx.Done()
		server.Shutdown(ctx)
	}()
}

// As soon as prometheus starts responding to http request should be able to accept Interrupt signals for a gracefull shutdown.
func TestStartupInterrupt(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	cmd := exec.Command(
		os.Args[0],
		append(e2eTestMainCommonFlags,
			"--prometheus.wal=testdata/wal",
			"--log.level=debug", // The tests below depend on debug logs
		)...)

	cmd.Env = append(os.Environ(), "RUN_MAIN=1")
	var bout, berr bytes.Buffer
	cmd.Stdout = &bout
	cmd.Stderr = &berr
	err := cmd.Start()
	if err != nil {
		t.Errorf("execution error: %v", err)
		return
	}

	done := make(chan error)
	go func() {
		done <- cmd.Wait()
	}()

	var startedOk bool
	var stoppedErr error

Loop:
	// This loop sleeps allows least 10 seconds to pass.
	for x := 0; x < 10; x++ {
		// Waits for the sidecar's /-/ready handler
		if resp, err := http.Get(e2eReadyURL); err == nil && resp.StatusCode/100 == 2 {
			startedOk = true
			cmd.Process.Signal(os.Interrupt)
			select {
			case stoppedErr = <-done:
				break Loop
			case <-time.After(10 * time.Second):
			}
			break Loop
		} else {
			select {
			case stoppedErr = <-done:
				break Loop
			default: // try again
			}
		}
		time.Sleep(time.Second)
	}

	if !startedOk {
		t.Errorf("opentelemetry-prometheus-sidecar didn't start in the specified timeout")
		return
	}
	if err := cmd.Process.Kill(); err == nil {
		t.Errorf("opentelemetry-prometheus-sidecar didn't shutdown after sending the Interrupt signal")
	}
	const expected = "Prometheus is not ready: context canceled"
	require.Error(t, stoppedErr)
	require.Contains(t, stoppedErr.Error(), "exit status 1")

	// Because the fake endpoint was started after the start of
	// the test, we should see some gRPC warnings the connection up
	// until --startup.timeout takes effect.
	require.Contains(t, berr.String(), expected)

	// The process should have been interrupted.
	require.Contains(t, berr.String(), "received SIGTERM, exiting")

	// The selftest was interrupted.
	require.Contains(t, berr.String(), "selftest failed, not starting")
}

func TestMainExitOnFailure(t *testing.T) {
	cmd := exec.Command(
		os.Args[0],
		"--totally-bogus-flag-name=testdata/wal",
	)

	cmd.Env = append(os.Environ(), "RUN_MAIN=1")
	var berr bytes.Buffer
	cmd.Stderr = &berr
	require.NoError(t, cmd.Start())

	require.Error(t, cmd.Wait())
	require.Contains(t, berr.String(), "totally-bogus-flag-name")
}

func TestParseFilters(t *testing.T) {
	for _, tt := range []struct {
		name         string
		filtersets   []string
		wantMatchers int
	}{
		{"just filtersets", []string{"metric_name"}, 1},
		{"no filtersets", []string{}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Test success cases.
			parsed, err := parseFilters(tt.filtersets)
			if err != nil {
				t.Fatal(err)
			}
			if len(parsed) != tt.wantMatchers {
				t.Fatalf("expected %d matchers; got %d", tt.wantMatchers, len(parsed))
			}
		})
	}

	// Test failure cases.
	for _, tt := range []struct {
		name       string
		filtersets []string
	}{
		{"Invalid operator in filterset", []string{`{a!=="1"}`}},
		{"Empty filterset", []string{""}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseFilters(tt.filtersets); err == nil {
				t.Fatalf("expected error, but got none")
			}
		})
	}
}

func TestStartupUnhealthyEndpoint(t *testing.T) {
	// Tests that the selftest detects an unhealthy endpoint during the selftest.
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	cmd := exec.Command(
		os.Args[0],
		append(e2eTestMainCommonFlags,
			"--prometheus.wal=testdata/wal",
			"--startup.timeout=5s",
			"--destination.timeout=1s",
		)...)

	cmd.Env = append(os.Environ(), "RUN_MAIN=1")
	var bout, berr bytes.Buffer
	cmd.Stdout = &bout
	cmd.Stderr = &berr
	err := cmd.Start()
	if err != nil {
		t.Errorf("execution error: %v", err)
		return
	}
	defer cmd.Wait()
	defer cmd.Process.Kill()

	ts := newTestServer(t, nil)
	defer ts.Stop()
	ts.runPrometheusService(promtest.Config{})

	cmd.Wait()

	t.Logf("stdout: %v\n", bout.String())
	t.Logf("stderr: %v\n", berr.String())

	require.Contains(t, berr.String(), "selftest failed, not starting")
	require.Contains(t, berr.String(), "selftest recoverable error, still trying")
}

func TestSuperStackDump(t *testing.T) {
	// Tests that the selftest detects an unhealthy endpoint during the selftest.
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	cmd := exec.Command(
		os.Args[0],
		append(e2eTestMainSupervisorFlags,
			"--destination.endpoint=http://127.0.0.1:19000",
			"--diagnostics.endpoint=http://127.0.0.1:19000",
			"--prometheus.wal=testdata/wal",
			"--healthcheck.period=1s",
		)...)

	ms := newTestServer(t, nil)
	ms.runMetricsService()

	// Note: there's no metadata api here, we'll see a "metadata
	// not found" failure in the log. We could fix this by configuring
	// matching metadata to what's in testdata/wal here.
	ms.runPrometheusService(promtest.Config{})

	ts := newTraceServer(t)
	ms.runDiagnosticsService(ts)

	cmd.Env = append(os.Environ(), "RUN_MAIN=1")
	var bout, berr bytes.Buffer
	cmd.Stdout = &bout
	cmd.Stderr = &berr
	err := cmd.Start()
	if err != nil {
		t.Errorf("execution error: %v", err)
		return
	}
	timer := time.NewTimer(time.Second * 10)
	defer timer.Stop()

	var lock sync.Mutex
	var diagSpans []*traces.ResourceSpans

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		lock.Lock()
		defer lock.Unlock()
		for {
			select {
			case <-timer.C:
				t.Log("timeout waiting for spans")
				t.FailNow()
				return
			case rs := <-ts.spans:
				// Note: below searching for a stack dump and
				// a few key strings, as a simple test.  TODO:
				// improve by testing support, factoring the
				// special-purpose logic here into another
				// package.
				diagSpans = append(diagSpans, rs)
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		for _ = range ms.metrics {
			// Dumping the metrics diagnostics here.
			// TODO: add testing support to validate
			// metrics from tests and then build more
			// tests based on metrics.
		}
	}()

	// The process is expected to kill itself.
	err = cmd.Wait()
	require.Error(t, err)

	cancel()
	wg.Wait()
	defer ms.Stop()
	defer ts.Stop()

	lock.Lock()
	defer lock.Unlock()

	foundCrash := false
	for _, rs := range diagSpans {
		for _, span := range rs.InstrumentationLibrarySpans[0].Spans {
			require.Contains(t, []string{"health-client", "shutdown-report"}, span.Name)

			if span.Name == "shutdown-report" {
				continue
			}
			sa := map[string]string{}
			for _, a := range span.Attributes {
				sa[a.Key] = a.Value.String()
			}
			statusCode := sa["http.status_code"]
			require.Contains(t, []string{
				"int_value:200",
				"int_value:503",
			}, statusCode)

			if statusCode == "int_value:200" {
				continue
			}

			foundCrash = true
			require.Contains(t, sa["sidecar.status"], "unhealthy")
			require.Contains(t, sa["sidecar.stackdump"], "goroutine")
			require.Contains(t, sa["sidecar.stackdump"], "net/http.(*ServeMux).ServeHTTP")
		}
	}

	require.True(t, foundCrash, "expected to find a crash report")
	require.Contains(t, berr.String(), "metadata not found")
}
