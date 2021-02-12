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
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	if os.Getenv("RUN_MAIN") == "" {
		// Run the test directly.
		os.Exit(m.Run())
	}

	main()
}

func runPrometheusService(ts *testServer) {
	mux := http.NewServeMux()
	mux.HandleFunc("/-/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	address := fmt.Sprint("0.0.0.0:19093")
	server := &http.Server{
		Addr:    address,
		Handler: mux,
	}
	ctx, cancel := context.WithCancel(context.Background())

	go server.ListenAndServe()

	go func() {
		<-ctx.Done()
		server.Shutdown(ctx)
	}()

	ts.stops <- cancel
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
			"--log.level=debug",
			"--startup.delay=1s",
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
	const expected = "source is not ready: context canceled"
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
	logger := log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
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
			parsed, err := parseFilters(logger, tt.filtersets)
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
			if _, err := parseFilters(logger, tt.filtersets); err == nil {
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
			"--log.level=debug",
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

	ts := newTestServer(t)
	defer ts.Stop()
	runPrometheusService(ts)

	cmd.Wait()

	t.Logf("stdout: %v\n", bout.String())
	t.Logf("stderr: %v\n", berr.String())

	require.Contains(t, berr.String(), "selftest failed, not starting")
	require.Contains(t, berr.String(), "selftest recoverable error, still trying")
}
