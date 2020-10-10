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
	"errors"
	"net/http"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/google/go-cmp/cmp"
	"github.com/lightstep/lightstep-prometheus-sidecar/metadata"
	"github.com/prometheus/prometheus/pkg/textparse"
)

func TestMain(m *testing.M) {
	if os.Getenv("RUN_MAIN") == "" {
		// Run the test directly.
		os.Exit(m.Run())
	}

	main()
}

// As soon as prometheus starts responding to http request should be able to accept Interrupt signals for a gracefull shutdown.
func TestStartupInterrupt(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	cmd := exec.Command(os.Args[0], "--prometheus.wal-directory=testdata/wal")
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
	for x := 0; x < 10; x++ {
		// error=nil means the sidecar has started so can send the interrupt signal and wait for the grace shutdown.
		if _, err := http.Get("http://localhost:9091/metrics"); err == nil {
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
		time.Sleep(500 * time.Millisecond)
	}

	t.Logf("stdout: %v\n", bout.String())
	t.Logf("stderr: %v\n", berr.String())
	if !startedOk {
		t.Errorf("prometheus-stackdriver-sidecar didn't start in the specified timeout")
		return
	}
	if err := cmd.Process.Kill(); err == nil {
		t.Errorf("prometheus-stackdriver-sidecar didn't shutdown gracefully after sending the Interrupt signal")
	} else if stoppedErr != nil && stoppedErr.Error() != "signal: interrupt" { // TODO - find a better way to detect when the process didn't exit as expected!
		t.Errorf("prometheus-stackdriver-sidecar exited with an unexpected error:%v", stoppedErr)
	}
}

func TestParseFiltersets(t *testing.T) {
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
			parsed, err := parseFiltersets(logger, tt.filtersets)
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
			if _, err := parseFiltersets(logger, tt.filtersets); err == nil {
				t.Fatalf("expected error, but got none")
			}
		})
	}
}

func TestProcessFileConfig(t *testing.T) {
	for _, tt := range []struct {
		name           string
		config         fileConfig
		renameMappings map[string]string
		staticMetadata []*metadata.Entry
		err            error
	}{
		{
			"empty",
			fileConfig{},
			map[string]string{},
			[]*metadata.Entry{},
			nil,
		},
		{
			"smoke",
			fileConfig{
				MetricRenames: []metricRenamesConfig{
					{From: "from", To: "to"},
				},
				StaticMetadata: []staticMetadataConfig{
					{Metric: "int64_counter", Type: "counter", ValueType: "int64", Help: "help1"},
					{Metric: "double_gauge", Type: "gauge", ValueType: "double", Help: "help2"},
					{Metric: "default_gauge", Type: "gauge"},
				},
			},
			map[string]string{"from": "to"},
			[]*metadata.Entry{
				&metadata.Entry{Metric: "int64_counter", MetricType: textparse.MetricTypeCounter, ValueType: metadata.INT64, Help: "help1"},
				&metadata.Entry{Metric: "double_gauge", MetricType: textparse.MetricTypeGauge, ValueType: metadata.DOUBLE, Help: "help2"},
				&metadata.Entry{Metric: "default_gauge", MetricType: textparse.MetricTypeGauge, ValueType: metadata.DOUBLE},
			},
			nil,
		},
		{
			"missing_metric_type",
			fileConfig{
				StaticMetadata: []staticMetadataConfig{{Metric: "int64_default", ValueType: "int64"}},
			},
			nil, nil,
			errors.New("invalid metric type \"\""),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			renameMappings, staticMetadata, err := processFileConfig(tt.config)
			fmt.Println("ERR", err)
			if diff := cmp.Diff(tt.renameMappings, renameMappings); diff != "" {
				t.Errorf("renameMappings mismatch: %v", diff)
			}
			if diff := cmp.Diff(tt.staticMetadata, staticMetadata); diff != "" {
				t.Errorf("staticMetadata mismatch: %v", diff)
			}
			if (tt.err != nil && err != nil && tt.err.Error() != err.Error()) ||
				(tt.err == nil && err != nil) || (tt.err != nil && err == nil) {
				t.Errorf("error mismatch: got %v, expected %v", err, tt.err)
			}
		})
	}
}
