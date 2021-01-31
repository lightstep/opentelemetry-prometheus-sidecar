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

package telemetry

// Note: Tracing has been disabled here and elsewhere in the main
// sidecar code.  However, we'd like to use it along with
// span-to-metrics to measure latency, throughput, and error rate of
// the outbound connections utilizing the tracing instrumentation.
// This could be done with an OTel-Go Metrics SDK adatper.  Since this
// would be more work, the simpler approach taken here uses a
// ValueRecorder instrument and a simple calling pattern.

import (
	"context"
	"time"

	sidecar "github.com/lightstep/opentelemetry-prometheus-sidecar"
	"go.opentelemetry.io/otel/label"
	"go.opentelemetry.io/otel/metric"
)

type (
	Timer metric.Float64ValueRecorder

	Timing struct {
		ctx     context.Context
		timer   Timer
		started time.Time
	}
)

func NewTimer(name, desc string) Timer {
	return Timer(sidecar.OTelMeterMust.NewFloat64ValueRecorder(
		name,
		metric.WithDescription(desc),
		metric.WithUnit("s"),
	))
}

func (t Timer) Start(ctx context.Context) Timing {
	return Timing{
		ctx:     ctx,
		timer:   t,
		started: time.Now(),
	}
}

func (t Timing) Stop(err *error, kvs ...label.KeyValue) {
	errorval := "false"
	if err != nil && *err != nil {
		errorval = "true"
	}

	kvs = append(kvs, label.String("error", errorval))

	metric.Float64ValueRecorder(t.timer).Record(t.ctx, time.Since(t.started).Seconds(), kvs...)
}
