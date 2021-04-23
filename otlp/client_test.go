// Copyright 2017 The Prometheus Authors
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

package otlp

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metricsService "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metric_pb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"golang.org/x/net/nettest"
	"google.golang.org/grpc"
)

func TestStoreErrorHandlingOnTimeout(t *testing.T) {
	listener, err := nettest.NewLocalListener("tcp")
	require.NoError(t, err)
	grpcServer := grpc.NewServer()
	metricsService.RegisterMetricsServiceServer(grpcServer, &metricServiceServer{
		status: nil,
	})
	go grpcServer.Serve(listener)
	defer grpcServer.Stop()

	serverURL, err := url.Parse("https://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	c := NewClient(ClientConfig{
		URL:     serverURL,
		Timeout: 0, // Immeditate Timeout.
	})
	err = c.Store(&metricsService.ExportMetricsServiceRequest{
		ResourceMetrics: []*metric_pb.ResourceMetrics{
			{},
		},
	})
	require.True(t, isRecoverable(err), "expected recoverableError in error %v", err)
}

func TestEmptyRequest(t *testing.T) {
	serverURL, err := url.Parse("http://localhost:12345")
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(ClientConfig{
		URL:     serverURL,
		Timeout: time.Second,
	})
	if err := c.Store(&metricsService.ExportMetricsServiceRequest{}); err != nil {
		t.Fatal(err)
	}
}

// Note: There is no test that the client correctly chooses the
// correct branch after the call to service.Export in Client.Store().
// This is deficient, however we are planning to replace this code
// with the OTel-Go OTLP Exporter, after which such a test would have
// to be rewritten from scratch.
