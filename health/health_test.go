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
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/label"
	"go.opentelemetry.io/otel/metric"
	metricsdk "go.opentelemetry.io/otel/sdk/export/metric"
	controller "go.opentelemetry.io/otel/sdk/metric/controller/basic"
	processor "go.opentelemetry.io/otel/sdk/metric/processor/basic"
	selector "go.opentelemetry.io/otel/sdk/metric/selector/simple"
)

type tester struct {
	*testing.T
	*Checker
	*controller.Controller
	processedInst metric.Int64Counter
	outcomeInst   metric.Int64Counter
	healthServer  *httptest.Server
	readyServer   *httptest.Server
}

func testController(t *testing.T) *tester {
	cont := controller.New(
		processor.New(
			selector.NewWithHistogramDistribution([]float64{
				0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
			}),
			metricsdk.CumulativeExportKindSelector(),
			processor.WithMemory(true),
		),
		controller.WithCollectPeriod(0),
	)
	provider := cont.MeterProvider()
	processed := metric.Must(provider.Meter("test")).NewInt64Counter(config.ProcessedMetric)
	outcome := metric.Must(provider.Meter("test")).NewInt64Counter(config.OutcomeMetric)

	checker := NewChecker(cont)

	healthServer := httptest.NewServer(checker.Health())
	readyServer := httptest.NewServer(checker.Ready())

	return &tester{
		T:             t,
		Checker:       checker,
		Controller:    cont,
		processedInst: processed,
		outcomeInst:   outcome,
		healthServer:  healthServer,
		readyServer:   readyServer,
	}
}

func (t *tester) Collect() {
	require.NoError(t.T, t.Controller.Collect(context.Background()))
}

func (t *tester) getHealth() (int, Response) {
	require.NoError(t.T, t.Controller.Collect(context.Background()))

	resp, err := http.Get(t.healthServer.URL)
	require.NoError(t.T, err)

	var res Response
	require.NoError(t.T, json.NewDecoder(resp.Body).Decode(&res))

	require.Equal(t.T, resp.StatusCode, res.Code)

	return resp.StatusCode, res
}

func TestProcessedProgress(t *testing.T) {
	// Try health check failures after 1, 2, and 3 healthy periods.
	for k := 1; k <= 3; k++ {
		ctx := context.Background()
		tester := testController(t)

		// For the number of healthy periods, add one at a time
		// and check for health.
		for j := 0; j < k; j++ {
			tester.processedInst.Add(ctx, 1)

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
				config.ProcessedMetric,
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
		tester.processedInst.Add(ctx, 1)

		code, result := tester.getHealth()

		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "healthy", result.Status)
	}

	for j := 0; j < numSamples/2; j++ {
		tester.outcomeInst.Add(ctx, 10, label.String("outcome", "failed"))
		tester.processedInst.Add(ctx, 1)

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
}

func TestOutcomes4951(t *testing.T) {
	ctx := context.Background()
	tester := testController(t)

	for j := 0; j < 100; j++ {
		tester.outcomeInst.Add(ctx, 51, label.String("outcome", "success"))
		tester.outcomeInst.Add(ctx, 49, label.String("outcome", fmt.Sprint(rand.Intn(10))))
		tester.processedInst.Add(ctx, 100)

		code, result := tester.getHealth()

		require.Equal(t, http.StatusOK, code)
		require.Equal(t, "healthy", result.Status)
	}
}
