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
)

type (
	Checker struct {
		ready
	}

	ready struct {
	}

	Response struct {
		Status string `json:"status"`
	}
)

func NewChecker() *Checker {
	return &Checker{}
}

func (c *Checker) Health() http.Handler {
	// TODO: Real health checking.
	return &c.ready
}

func (c *Checker) Ready() http.Handler {
	// TODO: Real readiness checking.
	return &c.ready
}

func (r *ready) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	status := http.StatusOK

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	json.NewEncoder(w).Encode(Response{
		Status: http.StatusText(status),
	})
}
