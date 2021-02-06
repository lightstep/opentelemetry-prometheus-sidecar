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
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/label"
	"go.opentelemetry.io/otel/metric"
	controller "go.opentelemetry.io/otel/sdk/metric/controller/basic"
)

type tester struct {
	*testing.T
	*Checker
	*controller.Controller
	producedInst metric.Int64Counter
	outcomeInst  metric.Int64Counter
	healthServer *httptest.Server
	readyServer  *httptest.Server
}

func testController(t *testing.T) *tester {
	cont := telemetry.InternalOnly().Controller
	provider := cont.MeterProvider()
	produced := metric.Must(provider.Meter("test")).NewInt64Counter(config.ProducedMetric)
	outcome := metric.Must(provider.Meter("test")).NewInt64Counter(config.OutcomeMetric)

	checker := NewChecker(cont)

	healthServer := httptest.NewServer(checker.Health())
	readyServer := httptest.NewServer(checker.Ready())

	return &tester{
		T:            t,
		Checker:      checker,
		Controller:   cont,
		producedInst: produced,
		outcomeInst:  outcome,
		healthServer: healthServer,
		readyServer:  readyServer,
	}
}

func (t *tester) Collect() {
	require.NoError(t.T, t.Controller.Collect(context.Background()))
}

func (t *tester) getHealth() (int, Response) {
	return t.getHealthFrom(true)
}

func (t *tester) getHealthUnsupervised() (int, Response) {
	return t.getHealthFrom(false)
}

func (t *tester) getHealthFrom(isSuper bool) (int, Response) {
	require.NoError(t.T, t.Controller.Collect(context.Background()))

	url := t.healthServer.URL

	if isSuper {
		url += "?supervisor=true"
	}

	resp, err := http.Get(url)
	require.NoError(t.T, err)

	var res Response
	require.NoError(t.T, json.NewDecoder(resp.Body).Decode(&res))

	require.Equal(t.T, resp.StatusCode, res.Code)

	return resp.StatusCode, res
}

func TestProducedProgress(t *testing.T) {
	// Try health check failures after 1, 2, and 3 healthy periods.
	for k := 1; k <= 3; k++ {
		ctx := context.Background()
		tester := testController(t)

		// For the number of healthy periods, add one at a time
		// and check for health.
		for j := 0; j < k; j++ {
			tester.producedInst.Add(ctx, 1)
			tester.outcomeInst.Add(ctx, 1, label.String("outcome", "success"))

			for i := 0; i < numSamples-1; i++ {
				code, result := tester.getHealth()

				require.Equal(t, http.StatusOK, code, "i/j %d/%d", i, j)
				require.Equal(t, "healthy", result.Status)
			}
		}

		code, result := tester.getHealth()

		require.Equal(t, http.StatusServiceUnavailable, code)
		require.Contains(t, result.Status,
			fmt.Sprintf("unhealthy: %s stopped moving at %d",
				config.ProducedMetric,
				k,
			),
		)
	}
}

func TestOutcomesProgress(t *testing.T) {
	ctx := context.Background()
	tester := testController(t)

	for j := 0; j < numSamples; j++ {
		tester.outcomeInst.Add(ctx, 10, label.String("outcome", "success"))
		tester.producedInst.Add(ctx, 1)

		code, result := tester.getHealth()

		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "healthy", result.Status)
	}

	for i := 0; i < 10; i++ {
		// These do not change results
		code, result := tester.getHealthUnsupervised()
		require.Equal(t, "healthy", result.Status)
		require.Equal(t, http.StatusOK, code)
	}

	for j := 0; j < numSamples/2; j++ {
		tester.outcomeInst.Add(ctx, 10, label.String("outcome", "failed"))
		tester.producedInst.Add(ctx, 1)

		code, result := tester.getHealth()

		require.Equal(t, http.StatusOK, code, "J %d", j)
		require.Equal(t, "healthy", result.Status)
	}

	code, result := tester.getHealth()

	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Contains(t, result.Status,
		fmt.Sprintf("unhealthy: %s high error ratio",
			config.OutcomeMetric,
		),
	)

	for i := 0; i < 10; i++ {
		// These do not change results or dump stacks.
		code, result := tester.getHealthUnsupervised()
		require.Equal(t, http.StatusServiceUnavailable, code)
		require.Equal(t, "", result.Stackdump)
	}
}

func TestOutcomes4951(t *testing.T) {
	ctx := context.Background()
	tester := testController(t)

	for j := 0; j < 100; j++ {
		tester.outcomeInst.Add(ctx, 51, label.String("outcome", "success"))
		tester.outcomeInst.Add(ctx, 49, label.String("outcome", fmt.Sprint(rand.Intn(10))))
		tester.producedInst.Add(ctx, 100)

		code, result := tester.getHealth()

		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "healthy", result.Status)
	}
}

func TestOutcomesNoSuccess(t *testing.T) {
	ctx := context.Background()
	tester := testController(t)

	for j := 0; j < numSamples-1; j++ {
		tester.outcomeInst.Add(ctx, 10, label.String("outcome", "failed"))
		tester.producedInst.Add(ctx, 1)

		code, result := tester.getHealth()

		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "healthy", result.Status)
	}

	code, result := tester.getHealth()

	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Contains(t, result.Status,
		fmt.Sprintf("unhealthy: %s{%s} stopped moving at %d",
			config.OutcomeMetric,
			outcomeGoodLabel,
			0,
		),
	)
}

func TestSuperStackdump(t *testing.T) {
	tester := testController(t)

	for i := 0; i < numSamples-1; i++ {
		code, result := tester.getHealth()

		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "healthy", result.Status)
		require.Equal(t, "", result.Stackdump)
	}

	for i := 0; i < stackdumpAfter-1; i++ {
		code, result := tester.getHealth()

		require.Equal(t, http.StatusServiceUnavailable, code)
		require.Equal(t, "", result.Stackdump)
	}

	code, result := tester.getHealth()

	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Contains(t, result.Stackdump, "goroutine")
	oldStack := result.Stackdump

	// The next result still has a stackdump, identical
	code, result = tester.getHealth()

	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Equal(t, oldStack, result.Stackdump)
}
