// Copyright The OpenTelemetry Authors
//
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

package health

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric/number"
	export "go.opentelemetry.io/otel/sdk/export/metric"
	"go.opentelemetry.io/otel/sdk/export/metric/aggregation"
	controller "go.opentelemetry.io/otel/sdk/metric/controller/basic"
)

const (
	outcomeGoodAttribute = string(config.OutcomeKey) + "=" + config.OutcomeSuccessValue

	// In the default configuration, these settings compute a 5
	// minute average:

	numSamples = 5
)

type (
	Checker struct {
		readyHandler ready
		aliveHandler alive

		logger            log.Logger
		period            time.Duration
		startTime         time.Time
		metricsController *controller.Controller

		lock         sync.Mutex
		isRunning    bool
		tracker      map[string]*metricTracker
		lastUpdate   time.Time
		lastResponse Response

		thresholdRatio float64
	}

	ready struct {
		*Checker
	}

	alive struct {
		*Checker
	}

	metricPair struct {
		match float64
		other float64
	}

	metricTracker struct {
		samples []metricPair
	}

	Response struct {
		Code      int                       `json:"code"`
		Status    string                    `json:"status"`
		Metrics   map[string][]exportRecord `json:"metrics"`
		Running   bool                      `json:"running"`
		Stackdump string                    `json:"stackdump"`
	}

	exportRecord struct {
		Labels string  `json:"labels"`
		Value  float64 `json:"value"`
	}
)

// NewChecker returns a new ready and liveness checkers based on
// state from the metrics controller.
func NewChecker(cont *controller.Controller, period time.Duration, logger log.Logger, thresholdRatio float64) *Checker {
	c := &Checker{
		logger:            logger,
		period:            period,
		startTime:         time.Now(),
		metricsController: cont,
		tracker:           map[string]*metricTracker{},
		lastResponse: Response{
			Code: http.StatusOK,
		},
		thresholdRatio: thresholdRatio,
	}
	c.readyHandler.Checker = c
	c.aliveHandler.Checker = c
	return c
}

// Alive returns a liveness handler.
func (c *Checker) Alive() http.Handler {
	return &c.aliveHandler
}

// SetRunning indicates when the process is ready.
func (c *Checker) SetRunning() {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.isRunning = true
}

// getMetrics scans the current metrics processor state, copies the
// `sidecar.*` metrics into the result, for use in the healtcheck
// body.
func (a *alive) getMetrics() (map[string][]exportRecord, error) {
	cont := a.metricsController
	ret := map[string][]exportRecord{}
	enc := attribute.DefaultEncoder()

	// Note: we use the latest checkpoint, which is computed
	// periodically for the OTLP metrics exporter.
	if err := cont.ForEach(export.CumulativeExportKindSelector(),
		func(rec export.Record) error {
			var num number.Number
			var err error

			desc := rec.Descriptor()
			agg := rec.Aggregation()

			// Only return sidecar metrics.
			if !strings.HasPrefix(desc.Name(), config.SidecarPrefix) {
				return nil
			}

			if s, ok := agg.(aggregation.Sum); ok {
				num, err = s.Sum()
			} else if lv, ok := agg.(aggregation.LastValue); ok {
				num, _, err = lv.LastValue()
			} else {
				// Do not use histograms for health checking.
				return nil
			}
			if err != nil {
				return err
			}
			value := num.CoerceToFloat64(desc.NumberKind())
			lstr := enc.Encode(rec.Labels().Iter())

			ret[desc.Name()] = append(ret[desc.Name()], exportRecord{
				Labels: lstr,
				Value:  value,
			})
			return nil
		},
	); err != nil {
		return nil, err
	}

	return ret, nil
}

// ServeHTTP implements a healthcheck handler that returns healthy as
// long as comparing the youngest and oldest of `numSamples`:
//
// 1. the number of samples produced must rise
// 2. the ratio of {outcome=success}/{*} >= 0.5 over `numSamples`
func (a *alive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ok(w, func() Response {
		var resp Response

		a.lock.Lock()
		defer a.lock.Unlock()

		if !a.isRunning {
			return Response{
				Code:   http.StatusOK,
				Status: "starting",
			}
		}

		shouldUpdate := false

		if a.period == 0 || a.lastUpdate.IsZero() || time.Since(a.lastUpdate) > a.period {
			a.lastUpdate = time.Now()
			shouldUpdate = true
		}

		if shouldUpdate {
			metrics, err := a.getMetrics()

			resp.Running = a.isRunning

			if err != nil {
				resp.Code = http.StatusInternalServerError
				resp.Status = fmt.Sprint("internal error: ", err)
			} else if err := a.check(metrics); err != nil {
				resp.Code = http.StatusServiceUnavailable
				resp.Status = fmt.Sprint("unhealthy: ", err)
				a.countFailure(&resp)
			} else {
				resp.Code = http.StatusOK
				resp.Status = "healthy"
				resp.Metrics = metrics
			}

			level.Debug(a.logger).Log(
				"msg", "health inspection",
				"status", resp.Status,
			)

			a.lastResponse = resp
		} else {
			resp = a.lastResponse
		}
		return resp
	})
}

// ServeHTTP implements a liveness handler that returns ready after
// SetReady(true) is called.
func (r *ready) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ok(w, func() Response {
		r.lock.Lock()
		defer r.lock.Unlock()

		if !r.isRunning {
			return Response{
				Code:   http.StatusServiceUnavailable,
				Status: "starting",
			}
		}
		return Response{
			Code:   http.StatusOK,
			Status: "running",
		}
	})
}

// ok returns a health check response as application/json content.
func ok(w http.ResponseWriter, f func() Response) {
	r := f()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(r.Code)

	_ = json.NewEncoder(w).Encode(r)
}

// check parses selected counter metrics and returns an error if the
// sidecar is unhealthy based on their values.
func (a *alive) check(metrics map[string][]exportRecord) error {
	sumWhere := func(name, labels string) *metricTracker {
		t, ok := a.tracker[name]
		if !ok {
			t = &metricTracker{}
			a.tracker[name] = t
		}
		var matchCount, otherCount float64
		for _, e := range metrics[name] {
			if e.Labels == labels {
				matchCount += e.Value
			} else {
				otherCount += e.Value
			}
		}
		t.update(matchCount, otherCount)
		return t
	}

	produced := sumWhere(config.ProducedPointsMetric, "")

	if produced.defined() && produced.matchDelta() == 0 {
		// if the sidecar has not been able to produced samples, it's
		// possible that all series are being filtered
		if produced.matchValue() == 0 {
			return errors.Errorf("no samples produced, check filter configuration")
		}
		return errors.Errorf(
			"%s stopped moving at %v",
			config.ProducedPointsMetric,
			produced.matchValue(),
		)
	}

	outcomes := sumWhere(config.OutcomeMetric, outcomeGoodAttribute)

	if outcomes.defined() {

		if outcomes.matchDelta() == 0 {
			return errors.Errorf("%s{%s} stopped moving at %v",
				config.OutcomeMetric,
				outcomeGoodAttribute,
				outcomes.matchValue(),
			)
		}

		goodRatio := outcomes.matchRatio()

		if !math.IsNaN(goodRatio) && goodRatio < a.Checker.thresholdRatio {
			errorRatio := (1 - goodRatio)
			return errors.Errorf(
				"%s high error ratio: %.2f%%",
				config.OutcomeMetric,
				errorRatio*100,
			)
		}
	}

	return nil
}

func (a *alive) countFailure(res *Response) {
	buf := make([]byte, 1<<14)
	sz := runtime.Stack(buf, true)
	res.Stackdump = string(buf[:sz])
}

// update adds one match/other pair to the tracker.
func (m *metricTracker) update(match, other float64) {
	if len(m.samples) == numSamples {
		copy(m.samples[:numSamples-1], m.samples[1:numSamples])
		m.samples = m.samples[:numSamples-1]
	}

	m.samples = append(m.samples, metricPair{
		match: match,
		other: other,
	})
}

// lastSample returns the oldest match/other pair.
func (m *metricTracker) firstSample() metricPair {
	return m.samples[0]
}

// lastSample returns the current match/other pair.
func (m *metricTracker) lastSample() metricPair {
	return m.samples[len(m.samples)-1]
}

// defined returns true if the samples slice is full of `numSamples` items.
func (m *metricTracker) defined() bool {
	return len(m.samples) == numSamples
}

// matchDelta returns the current difference between the oldest and
// newest sample.
func (m *metricTracker) matchDelta() float64 {
	return m.lastSample().match - m.firstSample().match
}

// matchValue returns the current value of the matched metric.
func (m *metricTracker) matchValue() float64 {
	return m.lastSample().match
}

// matchRatio returns the ratio of count that match the queried attributes
// compared with the total including matches plus non-matches.
func (m *metricTracker) matchRatio() float64 {
	last := m.lastSample()
	first := m.firstSample()
	mdiff := last.match - first.match
	odiff := last.other - first.other
	return mdiff / (mdiff + odiff)
}

// MetricLogSummary returns a slice of pairs for the log.Logger.Log()
// API based on the metric name.
func (r *Response) MetricLogSummary(name string) (pairs []interface{}) {
	for _, e := range r.Metrics[name] {
		pairs = append(
			pairs,
			fmt.Sprint(
				name[len(config.SidecarPrefix):],
				// The log package strips `=`, replace with `:` instead.
				"{", strings.Replace(e.Labels, "=", ":", -1), "}",
			),
			uint64(e.Value))
	}
	return
}
