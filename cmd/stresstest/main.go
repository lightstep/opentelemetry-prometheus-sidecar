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
	"net/url"
	"os"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/cmd/internal"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/common"
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

	scfg := internal.SidecarConfig{
		Logger:     internal.NewLogger(cfg, false),
		MainConfig: cfg,
		InstanceId: "stresstest-prometheus-sidecar-001",
	}

	telemetry.StaticSetup(scfg.Logger)

	level.Info(scfg.Logger).Log("msg", "stresstest starting")

	telem := internal.StartTelemetry(
		scfg,
		"stresstest-prometheus-sidecar",
		false,
	)
	if telem != nil {
		defer telem.Shutdown(context.Background())
	}

	outputURL, _ := url.Parse(scfg.Destination.Endpoint)

	scf := internal.NewOTLPClientFactory(otlp.ClientConfig{
		Logger:           log.With(scfg.Logger, "component", "storage"),
		URL:              outputURL,
		Timeout:          scfg.Destination.Timeout.Duration,
		RootCertificates: scfg.Security.RootCertificates,
		Headers:          grpcMetadata.New(scfg.Destination.Headers),
		FailingReporter:  common.NewFailingSet(scfg.Logger),
	})

	queueManager, _ := otlp.NewQueueManager(
		log.With(scfg.Logger, "component", "queue_manager"),
		scfg.QueueConfig(),
		scfg.Destination.Timeout.Duration,
		scf,
		&fakeTailer{time.Now()},
		otlptest.Resource(
			otlptest.KeyValue("A", "B"),
		),
	)

	if err := queueManager.Start(); err != nil {
		level.Error(scfg.Logger).Log("could not start queue manager")
	}

	defer queueManager.Stop()

	now := time.Now()
	m := otlptest.IntGauge(
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
	)

	ctx := context.Background()
	for {
		queueManager.Append(ctx, m)
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
