/*
Copyright 2018 Google Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metadata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	"github.com/lightstep/opentelemetry-prometheus-sidecar/common"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/prometheus/prometheus/pkg/textparse"
)

type metadataResponse common.MetadataAPIResponse

func TestCache_Get(t *testing.T) {
	metrics := []common.APIMetadata{
		{Metric: "metric1", Type: textparse.MetricTypeCounter, Help: "help_metric1"},
		{Metric: "metric2", Type: textparse.MetricTypeGauge, Help: "help_metric2"},
		{Metric: "metric3", Type: textparse.MetricTypeHistogram, Help: "help_metric3"},
		{Metric: "metric4", Type: textparse.MetricTypeSummary, Help: "help_metric4"},
		{Metric: "metric5", Type: textparse.MetricTypeUnknown, Help: "help_metric5"},
		{Metric: "metric6", Type: config.MetricTypeUntyped, Help: "help_metric6"},
		{Metric: "metric_with_override", Type: textparse.MetricTypeGauge, Help: "help_metric_with_override"},
	}
	var handler func(qMetric, qMatch string) *metadataResponse

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := json.NewEncoder(w).Encode(handler(
			r.FormValue("metric"),
			r.FormValue("match_target"),
		))
		if err != nil {
			t.Fatal(err)
		}
	}))
	expect := func(want common.APIMetadata, got *config.MetadataEntry) {
		if !reflect.DeepEqual(want, common.APIMetadata{
			Metric: got.Metric,
			Type:   got.MetricType,
			Help:   got.Help,
		}) {
			t.Errorf("unexpected result %v, want %v", got, want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	// Create cache with static metadata.
	staticMetadata := []*config.MetadataEntry{
		&config.MetadataEntry{Metric: "static_metric1", MetricType: textparse.MetricTypeCounter, ValueType: config.INT64, Help: "help_static1"},
		&config.MetadataEntry{Metric: "static_metric2", MetricType: textparse.MetricTypeCounter, ValueType: config.DOUBLE, Help: "help_static2"},
		&config.MetadataEntry{Metric: "metric_with_override", MetricType: textparse.MetricTypeCounter, ValueType: config.INT64, Help: "help_metric_override"},
	}
	c := NewCache(nil, u, staticMetadata)

	// First get for the job, we expect an initial batch request.
	handler = func(qMetric, qMatch string) *metadataResponse {
		if qMetric != "" {
			t.Fatalf("unexpected metric %v in request", qMetric)
		}
		if qMatch != `{job="prometheus",instance="localhost:9090"}` {
			t.Fatalf("unexpected matcher %v in request", qMatch)
		}
		return &metadataResponse{Status: "success", Data: metrics[:4]}
	}
	md, err := c.Get(ctx, "prometheus", "localhost:9090", "metric2")
	if err != nil {
		t.Fatal(err)
	}
	expect(metrics[1], md)

	// Query metric that should have been retrieved in the initial batch.
	handler = func(qMetric, qMatch string) *metadataResponse {
		t.Fatal("unexpected request")
		return nil
	}
	md, err = c.Get(ctx, "prometheus", "localhost:9090", "metric1")
	if err != nil {
		t.Fatal(err)
	}
	expect(metrics[0], md)
	// Similarly, changing the instance should not trigger a fetch with a known metric and job.
	md, err = c.Get(ctx, "prometheus", "localhost:8000", "metric3")
	if err != nil {
		t.Fatal(err)
	}
	expect(metrics[2], md)

	// Query metric that was not in the batch, expect a single-metric query.
	handler = func(qMetric, qMatch string) *metadataResponse {
		if qMetric != "metric5" {
			t.Fatalf("unexpected metric %v in request", qMetric)
		}
		if qMatch != `{job="prometheus",instance="localhost:9090"}` {
			t.Fatalf("unexpected matcher %v in request", qMatch)
		}
		return &metadataResponse{Status: "success", Data: metrics[4:5]}
	}
	md, err = c.Get(ctx, "prometheus", "localhost:9090", "metric5")
	if err != nil {
		t.Fatal(err)
	}
	expect(metrics[4], md)
	// It should be in our cache afterwards.
	handler = func(qMetric, qMatch string) *metadataResponse {
		t.Fatal("unexpected request")
		return nil
	}
	md, err = c.Get(ctx, "prometheus", "localhost:9090", "metric5")
	if err != nil {
		t.Fatal(err)
	}
	expect(metrics[4], md)

	// Test "untyped" metric type from Prometheus 2.4.
	handler = func(qMetric, qMatch string) *metadataResponse {
		if qMetric != "metric6" {
			t.Fatalf("unexpected metric %v in request", qMetric)
		}
		if qMatch != `{job="prometheus",instance="localhost:9090"}` {
			t.Fatalf("unexpected matcher %v in request", qMatch)
		}
		return &metadataResponse{Status: "success", Data: metrics[5:6]}
	}
	md, err = c.Get(ctx, "prometheus", "localhost:9090", "metric6")
	if err != nil {
		t.Fatal(err)
	}
	expect(common.APIMetadata{Metric: "metric6", Type: textparse.MetricTypeUnknown, Help: "help_metric6"}, md)

	// The scrape layer's metrics should not fire off requests.
	for _, internalName := range []string{"up", "scrape_series_added"} {
		md, err = c.Get(ctx, "prometheus", "localhost:9090", internalName)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(internalMetrics[internalName], md) {
			t.Errorf("unexpected metadata %v, want %v", *md, internalMetrics[internalName])
		}
		md, err = c.Get(ctx, "prometheus", "localhost:9090", internalName)
	}

	// If a metric does not exist, we first expect a fetch attempt.
	handler = func(qMetric, qMatch string) *metadataResponse {
		if qMetric != "does_not_exist" {
			t.Fatalf("unexpected metric %v in request", qMetric)
		}
		if qMatch != `{job="prometheus",instance="localhost:9090"}` {
			t.Fatalf("unexpected matcher %v in request", qMatch)
		}
		return &metadataResponse{Status: "error", ErrorType: apiErrorNotFound, Error: "does not exist"}
	}
	md, err = c.Get(ctx, "prometheus", "localhost:9090", "does_not_exist")
	if err != nil {
		t.Fatal(err)
	}
	if md != nil {
		t.Fatalf("expected nil metadata but got %v", md)
	}
	// Requesting it again should not do another request (modulo timeout).
	handler = func(qMetric, qMatch string) *metadataResponse {
		t.Fatal("unexpected request")
		return nil
	}
	md, err = c.Get(ctx, "prometheus", "localhost:9090", "does_not_exist")
	if err != nil {
		t.Fatal(err)
	}
	if md != nil {
		t.Fatalf("expected nil metadata but got %v", md)
	}

	// Test matcher escaping.
	handler = func(qMetric, qMatch string) *metadataResponse {
		if qMatch != `{job="prometheus\nwith_newline",instance="localhost:9090"}` {
			t.Fatalf("matcher not escaped properly: %s", qMatch)
		}
		return nil
	}
	_, err = c.Get(ctx, "prometheus\nwith_newline", "localhost:9090", "metric")
	if err != nil {
		t.Fatal(err)
	}

	// Test fallthrough to static metadata.
	handler = func(qMetric, qMatch string) *metadataResponse {
		return nil
	}
	md, err = c.Get(ctx, "prometheus", "localhost:9090", "static_metric2")
	if err != nil {
		t.Fatal(err)
	}
	want := staticMetadata[1]
	if !reflect.DeepEqual(md, want) {
		t.Errorf("expected metadata %v but got %v", want, md)
	}

	// Test override with static metadata.
	handler = func(qMetric, qMatch string) *metadataResponse {
		return &metadataResponse{Status: "success", Data: metrics}
	}
	md, err = c.Get(ctx, "prometheus", "localhost:9090", "metric_with_override")
	if err != nil {
		t.Fatal(err)
	}
	want = staticMetadata[2]
	if !reflect.DeepEqual(md, want) {
		t.Errorf("expected metadata %v but got %v", want, md)
	}

	// Test recording rule.
	handler = func(qMetric, qMatch string) *metadataResponse {
		return nil
	}
	md, err = c.Get(ctx, "prometheus", "localhost:9090", "some:recording:rule")
	if err != nil {
		t.Fatal(err)
	}
	want = &config.MetadataEntry{
		Metric:     "some:recording:rule",
		MetricType: textparse.MetricTypeGauge,
	}
	if !reflect.DeepEqual(md, want) {
		t.Errorf("expected metadata %v but got %v", want, md)
	}
}

func TestNewCache(t *testing.T) {
	static := []*config.MetadataEntry{
		&config.MetadataEntry{Metric: "a", Help: "a"},
		&config.MetadataEntry{Metric: "b", Help: "b"},
	}
	c := NewCache(nil, nil, static)

	want := map[string]*config.MetadataEntry{
		"a": &config.MetadataEntry{Metric: "a", Help: "a"},
		"b": &config.MetadataEntry{Metric: "b", Help: "b"},
	}
	if !reflect.DeepEqual(c.staticMetadata, want) {
		t.Errorf("expected metadata %v but got %v", want, c.staticMetadata)
	}
}
