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
	"strings"
	"sync/atomic"

	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/label"
	"go.opentelemetry.io/otel/metric/number"
	export "go.opentelemetry.io/otel/sdk/export/metric"
	"go.opentelemetry.io/otel/sdk/export/metric/aggregation"
	controller "go.opentelemetry.io/otel/sdk/metric/controller/basic"
)

const (
	// TODO move constants around here and in supervisor.go
	processedMetric = "sidecar.sampled.processed"
	outcomeMetric   = "sidecar.queue.outcome"

	outcomeGoodLabel = "outcome=success"

	numSamples = 5

	thresholdRatio = 0.5
)

type (
	Checker struct {
		ready
		healthy
	}

	ready struct {
		atomic.Value
	}

	healthy struct {
		*controller.Controller
		tracker map[string]*metricTracker
	}

	metricPair struct {
		match float64
		other float64
	}

	metricTracker struct {
		samples []metricPair
	}

	Response struct {
		Code    int                       `json:"code"`
		Status  string                    `json:"status"`
		Metrics map[string][]exportRecord `json:"metrics"`
	}

	exportRecord struct {
		Labels string  `json:"labels"`
		Value  float64 `json:"value"`
	}
)

// NewChecker returns a new health and readiness checker based on
// state from the metrics controller.
func NewChecker(cont *controller.Controller) *Checker {
	c := &Checker{
		healthy: healthy{
			Controller: cont,
			tracker:    map[string]*metricTracker{},
		},
	}
	c.ready.Value.Store(false)
	return c
}

// Health returns a healthcheck handler.
func (c *Checker) Health() http.Handler {
	return &c.healthy
}

// Ready returns a readiness handler.
func (c *Checker) Ready() http.Handler {
	return &c.ready
}

// SetReady indicates when the process is ready.
func (c *Checker) SetReady(ready bool) {
	c.ready.Value.Store(ready)
}

// getMetrics scans the current metrics processor state, copies the
// `sidecar.*` metrics into the result, for use in the healtcheck
// body.
func (h *healthy) getMetrics() (map[string][]exportRecord, error) {
	cont := h.Controller
	ret := map[string][]exportRecord{}
	enc := label.DefaultEncoder()

	// Note: we use the last collected value, since the controller
	// is pushing metrics.

	if err := cont.ForEach(export.CumulativeExportKindSelector(),
		func(rec export.Record) error {
			var num number.Number
			var err error

			desc := rec.Descriptor()
			agg := rec.Aggregation()

			// Only return sidecar metrics.
			if !strings.HasPrefix(desc.Name(), "sidecar.") {
				return nil
			}

			if s, ok := agg.(aggregation.Sum); ok {
				num, err = s.Sum()
			} else if lv, ok := agg.(aggregation.LastValue); ok {
				num, _, err = lv.LastValue()
			} else {
				// We expect to skip histograms here.
				// Note the copyToCounter processor
				// ensures we see these as counts
				// anyway.
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
// 1. the number of samples processed must rise
// 2. the ratio of {outcome=success}/{*} >= 0.5 over `numSamples`
func (h *healthy) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	ok(w, func() Response {
		var code int
		var status string

		metrics, err := h.getMetrics()

		if err != nil {
			code = http.StatusInternalServerError
			status = fmt.Sprint("internal error: ", err)
		} else if err := h.check(metrics); err != nil {
			code = http.StatusServiceUnavailable
			status = fmt.Sprint("unhealthy: ", err)
		} else {
			code = http.StatusOK
			status = "healthy"
		}

		return Response{
			Code:    code,
			Status:  status,
			Metrics: metrics,
		}
	})
}

// ServeHTTP implements a readiness handler that returns ready after
// SetReady(true) is called.
func (r *ready) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	ok(w, func() Response {
		code := http.StatusServiceUnavailable
		status := "starting"

		if r.Value.Load().(bool) {
			code = http.StatusOK
			status = "running"
		}

		return Response{
			Code:   code,
			Status: status,
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
func (h *healthy) check(metrics map[string][]exportRecord) error {
	sumWhere := func(name, labels string) *metricTracker {
		t, ok := h.tracker[name]
		if !ok {
			t = &metricTracker{}
			h.tracker[name] = t
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

	processed := sumWhere(processedMetric, "")

	if processed.defined() && processed.matchDelta() == 0 {
		return errors.Errorf(
			"sidecar.samples.processed stopped moving at %v",
			processed.matchValue(),
		)
	}

	outcomes := sumWhere(outcomeMetric, outcomeGoodLabel)

	if outcomes.defined() {

		goodRatio := outcomes.matchRatio()

		if !math.IsNaN(goodRatio) && goodRatio < thresholdRatio {
			errorRatio := (1 - goodRatio)
			return errors.Errorf(
				"sidecar.queue.outcome high error ratio: %.2f%%",
				errorRatio*100,
			)
		}
	}

	return nil
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

// matchRatio returns the ratio of count that match the queried labels
// compared with the total including matches plus non-matches.
func (m *metricTracker) matchRatio() float64 {
	last := m.lastSample()
	first := m.firstSample()
	mdiff := last.match - first.match
	odiff := last.other - first.other
	return mdiff / (mdiff + odiff)
}

// MetricLogSummary returns a slice of pairs for the log.Logger.Log() API
// based on the metric name suffix prefixed by `sidecar.`.
func (r *Response) MetricLogSummary(suffix string) (pairs []interface{}) {
	var mname = "sidecar." + suffix

	for _, e := range r.Metrics[mname] {
		pairs = append(
			pairs,
			// The log package strips `=`, replace with `:` instead.
			fmt.Sprint(suffix, "{", strings.Replace(e.Labels, "=", ":", -1), "}"), e.Value)
	}
	return
}
