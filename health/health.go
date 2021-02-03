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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"go.opentelemetry.io/otel/label"
	"go.opentelemetry.io/otel/metric/number"
	export "go.opentelemetry.io/otel/sdk/export/metric"
	"go.opentelemetry.io/otel/sdk/export/metric/aggregation"
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
		*telemetry.Telemetry
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

func NewChecker(telem *telemetry.Telemetry) *Checker {
	c := &Checker{
		healthy: healthy{
			Telemetry: telem,
		},
	}
	c.ready.Value.Store(false)
	return c
}

func (c *Checker) Health() http.Handler {
	return &c.healthy
}

func (c *Checker) Ready() http.Handler {
	return &c.ready
}

func (c *Checker) SetReady(ready bool) {
	c.ready.Value.Store(ready)
}

func (h *healthy) getMetrics() (map[string][]exportRecord, error) {
	cont := h.Telemetry.Controller
	ret := map[string][]exportRecord{}
	ctx := context.Background()

	if err := cont.Collect(ctx); err != nil {
		return nil, err
	}

	enc := label.DefaultEncoder()

	if err := cont.ForEach(export.CumulativeExportKindSelector(),
		func(rec export.Record) error {
			var num number.Number
			var err error

			desc := rec.Descriptor()
			agg := rec.Aggregation()

			if s, ok := agg.(aggregation.Sum); ok {
				num, err = s.Sum()
			} else if lv, ok := agg.(aggregation.LastValue); ok {
				num, _, err = lv.LastValue()
			} else {
				fmt.Printf("LOOK1 %T %v\n", agg, agg)
				return nil
			}
			if err != nil {
				fmt.Printf("LOOK2 %T %v\n", agg, agg)
				return err
			}
			value := num.CoerceToFloat64(desc.NumberKind())

			ret[desc.Name()] = append(ret[desc.Name()], exportRecord{
				Labels: enc.Encode(rec.Labels().Iter()),
				Value:  value,
			})
			return nil
		},
	); err != nil {
		return nil, err
	}

	return ret, nil
}

func (h *healthy) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	ok(w, func() Response {
		var code int
		var status string

		metrics, err := h.getMetrics()

		fmt.Printf("METRICS SENT %v\n", metrics)

		if err != nil {
			code = http.StatusServiceUnavailable
			status = err.Error()
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

func ok(w http.ResponseWriter, f func() Response) {
	r := f()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(r.Code)

	_ = json.NewEncoder(w).Encode(r)
}
