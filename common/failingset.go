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

package common

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	sidecar "github.com/lightstep/opentelemetry-prometheus-sidecar"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"go.opentelemetry.io/otel/metric"
)

type (
	// FailingReporter is an interface for FailingSet
	FailingReporter interface {
		Set(reason, metricName string)
	}

	// FailingSet reports a set of gauges to describe failing data points.
	FailingSet struct {
		observer metric.Int64ValueObserver
		logger   log.Logger

		lock  sync.Mutex
		short stateMap
		long  stateMap

		lastSummary time.Time
	}

	stateMap map[string]nameMap
	nameMap  map[string]struct{}
)

const (
	failingConstant = 1

	failingMetricSummaryInterval = time.Minute * 5
)

func NewFailingSet(logger log.Logger) *FailingSet {
	i := &FailingSet{
		short:  stateMap{},
		long:   stateMap{},
		logger: logger,
	}
	i.observer = sidecar.OTelMeterMust.NewInt64ValueObserver(
		config.FailingMetricsMetric,
		i.observe,
		metric.WithDescription("labeled examples of failing metric data"),
	)
	return i

}

func (i *FailingSet) Set(reason, metricName string) {
	i.lock.Lock()
	defer i.lock.Unlock()

	i.short.set(reason, metricName)
	i.long.set(reason, metricName)
}

func (s stateMap) set(reason, metricName string) {
	if s[reason] == nil {
		s[reason] = nameMap{}
	}
	s[reason][metricName] = struct{}{}
}

func (i *FailingSet) observe(_ context.Context, result metric.Int64ObserverResult) {
	summary := i.observeLocked(result)

	if summary == nil {
		return
	}

	for reason, nm := range summary {
		var names []string
		for name := range nm {
			names = append(names, name)
		}
		sort.Strings(names)
		level.Warn(i.logger).Log(
			"reason", strings.ReplaceAll(reason, "-", " "),
			"names", fmt.Sprint(names),
		)
	}
}

func (i *FailingSet) observeLocked(result metric.Int64ObserverResult) stateMap {
	i.lock.Lock()
	defer i.lock.Unlock()

	for reason, names := range i.short {
		for metricName := range names {
			result.Observe(failingConstant,
				DroppedKeyReason.String(reason),
				MetricNameKey.String(metricName),
			)
		}
	}
	i.short = stateMap{}

	if len(i.long) == 0 {
		return nil
	}

	now := time.Now()
	if now.Sub(i.lastSummary) < failingMetricSummaryInterval {
		return nil
	}

	summary := i.long
	i.long = stateMap{}
	i.lastSummary = now
	return summary
}
