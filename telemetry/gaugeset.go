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
	"sync"

	sidecar "github.com/lightstep/opentelemetry-prometheus-sidecar"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// GaugeSet is a synchronous gauge instrument that reports a set of
// gauges through a ValueObserver.  (This use-case proves the OTel API
// model is lacking one instrument.)
//
// The application in this package does not care what value is
// reported, so we arbitrarily report a 1.
type (
	GaugeSet struct {
		observer metric.Int64ValueObserver

		lock  sync.Mutex
		state stateMap
	}

	stateMap map[attribute.Distinct]*attribute.Set
)

const gaugeConstant = 1

func NewGaugeSet(name, desc string) *GaugeSet {
	l := &GaugeSet{
		state: stateMap{},
	}
	l.observer = sidecar.OTelMeterMust.NewInt64ValueObserver(
		name,
		l.observe,
		metric.WithDescription(desc),
	)
	return l

}

func (l *GaugeSet) Set(attrs ...attribute.KeyValue) {
	lset := attribute.NewSet(attrs...)

	l.lock.Lock()
	defer l.lock.Unlock()

	if _, ok := l.state[lset.Equivalent()]; !ok {
		l.state[lset.Equivalent()] = &lset
	}
}

func (l *GaugeSet) observe(_ context.Context, result metric.Int64ObserverResult) {
	l.lock.Lock()
	defer l.lock.Unlock()

	for _, lset := range l.state {
		result.Observe(gaugeConstant, lset.ToSlice()...)
	}

	l.state = stateMap{}
}
