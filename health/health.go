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
	"net/http"
	"sync/atomic"
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
	}

	Response struct {
		Status string `json:"status"`
	}
)

func NewChecker() *Checker {
	c := &Checker{}
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

func (h *healthy) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	// TODO: Real health checking.
	ok(w, true)
}

func (r *ready) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	ok(w, r.Value.Load().(bool))
}

func ok(w http.ResponseWriter, ok bool) {
	code := http.StatusServiceUnavailable
	status := "starting"

	if ok {
		code = http.StatusOK
		status = "running"
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)

	_ = json.NewEncoder(w).Encode(Response{
		Status: status,
	})
}
