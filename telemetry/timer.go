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

import (
	"context"
	"time"

	sidecar "github.com/lightstep/opentelemetry-prometheus-sidecar"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type (
	// Timer is a simple instrument for measuring a section of
	// code, a kind of light-weight span that outputs a
	// ValueRecorder of its duration.
	Timer metric.Float64ValueRecorder

	Timing struct {
		ctx     context.Context
		timer   Timer
		started time.Time
	}

	Counter metric.Int64Counter
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

func (t Timing) Stop(err *error, kvs ...attribute.KeyValue) {
	errorval := "false"
	if err != nil && *err != nil {
		errorval = "true"
	}

	kvs = append(kvs, attribute.String("error", errorval))

	metric.Float64ValueRecorder(t.timer).Record(t.ctx, time.Since(t.started).Seconds(), kvs...)
}

func NewCounter(name, desc string) Counter {
	return Counter(sidecar.OTelMeterMust.NewInt64Counter(
		name,
		metric.WithDescription(desc),
	))
}

func (c Counter) Add(ctx context.Context, cnt int64, err *error, kvs ...attribute.KeyValue) {
	errorval := "false"
	if err != nil && *err != nil {
		errorval = "true"
	}

	kvs = append(kvs, attribute.String("error", errorval))

	metric.Int64Counter(c).Add(ctx, cnt, kvs...)
}
