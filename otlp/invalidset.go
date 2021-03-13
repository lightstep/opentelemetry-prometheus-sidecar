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

package otlp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	sidecar "github.com/lightstep/opentelemetry-prometheus-sidecar"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/common"
	"go.opentelemetry.io/otel/metric"
)

type (
	// InvalidSet reports a set of gauges to describe invalid data points.
	InvalidSet struct {
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
	invalidConstant = 1

	invalidMetricSummaryInterval = time.Minute * 5
)

func NewInvalidSet(logger log.Logger) *InvalidSet {
	i := &InvalidSet{
		short:  stateMap{},
		long:   stateMap{},
		logger: logger,
	}
	i.observer = sidecar.OTelMeterMust.NewInt64ValueObserver(
		"sidecar.metrics.invalid",
		i.observe,
		metric.WithDescription("labeled examples of invalid metric data"),
	)
	return i

}

func (i *InvalidSet) Set(reason, metricName string) {
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

func (i *InvalidSet) observe(_ context.Context, result metric.Int64ObserverResult) {
	summary := i.observeLocked(result)

	if summary == nil {
		return
	}

	for reason, nm := range summary {
		var names []string
		for name := range nm {
			names = append(names, name)
		}
		level.Warn(i.logger).Log(
			"reason", strings.ReplaceAll(reason, "-", " "),
			"names", fmt.Sprint(names),
		)
	}
}

func (i *InvalidSet) observeLocked(result metric.Int64ObserverResult) stateMap {
	i.lock.Lock()
	defer i.lock.Unlock()

	for reason, names := range i.short {
		for metricName := range names {
			result.Observe(invalidConstant,
				common.DroppedKeyReason.String(reason),
				metricNameKey.String(metricName),
			)
		}
	}
	i.short = stateMap{}

	now := time.Now()
	if now.Sub(i.lastSummary) < invalidMetricSummaryInterval {
		return nil
	}

	summary := i.long
	i.long = stateMap{}
	i.lastSummary = now
	return summary
}