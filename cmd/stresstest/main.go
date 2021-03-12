// Copyright Lightstep Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/url"
	"os"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/cmd/internal"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/internal/otlptest"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/otlp"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	grpcMetadata "google.golang.org/grpc/metadata"
)

type (
	fakeTailer struct {
		start time.Time
	}
)

var _ otlp.LogReaderClient = &fakeTailer{}

func main() {
	if !Main() {
		os.Exit(1)
	}
}

func usage(err error) {
	fmt.Fprintf(
		os.Stderr,
		"usage: %s TODO: %v\n",
		os.Args[0],
		err,
	)
}

func Main() bool {
	// Configure a from flags and/or a config file.
	cfg, _, _, err := config.Configure(os.Args, ioutil.ReadFile)
	if err != nil {
		usage(err)
		return false
	}

	logger := internal.NewLogger(cfg, false)

	telemetry.StaticSetup(logger)

	level.Info(logger).Log("msg", "stresstest starting")

	telem := internal.StartTelemetry(
		cfg,
		"stresstest-prometheus-sidecar",
		"stresstest-prometheus-sidecar-001",
		false,
		logger,
	)
	if telem != nil {
		defer telem.Shutdown(context.Background())
	}

	outputURL, _ := url.Parse(cfg.Destination.Endpoint)

	scf := internal.NewOTLPClientFactory(otlp.ClientConfig{
		Logger:           log.With(logger, "component", "storage"),
		URL:              outputURL,
		Timeout:          cfg.Destination.Timeout.Duration,
		RootCertificates: cfg.Security.RootCertificates,
		Headers:          grpcMetadata.New(cfg.Destination.Headers),
		InvalidSet:       otlp.NewInvalidSet(logger),
	})

	queueManager, err := otlp.NewQueueManager(
		log.With(logger, "component", "queue_manager"),
		cfg.QueueConfig(),
		cfg.Destination.Timeout.Duration,
		scf,
		&fakeTailer{time.Now()},
	)

	if err := queueManager.Start(); err != nil {
		level.Error(logger).Log("could not start queue manager")
	}

	defer queueManager.Stop()

	now := time.Now()
	req := otlptest.ResourceMetrics(
		otlptest.Resource(
			otlptest.KeyValue("A", "B"),
		),
		otlptest.InstrumentationLibraryMetrics(
			otlptest.InstrumentationLibrary(
				"testlib", "v0.0.1",
			),
			otlptest.IntGauge(
				"up",
				"d",
				"u",
				otlptest.IntDataPoint(
					otlptest.Labels(
						otlptest.Label("L", "M"),
					),
					now.Add(-time.Second),
					now,
					1,
				),
			),
		),
	)

	ctx := context.Background()
	for {
		queueManager.Append(ctx, rand.Uint64(), req)
	}
}

const (
	// Start behind and barely able to catch up
	initialSize = 256e6
	promRate    = 1.00e6
	readRate    = 1.01e6
)

func (t *fakeTailer) elapsed() float64 {
	return time.Since(t.start).Seconds()
}

func (t *fakeTailer) Size() (int, error) {
	return int(initialSize + t.elapsed()*promRate), nil
}

func (t *fakeTailer) Offset() int {
	return int(initialSize + t.elapsed()*readRate)
}
