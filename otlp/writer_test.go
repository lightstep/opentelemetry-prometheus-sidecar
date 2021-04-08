/*
Copyright 2019 Google Inc.

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

package otlp

import (
	"bytes"
	"testing"

	"github.com/go-kit/kit/log"
	"github.com/golang/protobuf/proto"
	metricsService "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metric_pb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

type myWriterCloser struct {
	Buffer bytes.Buffer
}

func (m *myWriterCloser) Write(p []byte) (int, error) {
	return m.Buffer.Write(p)
}

func (m *myWriterCloser) Close() error {
	m.Buffer.Reset()
	return nil
}

func TestRequest(t *testing.T) {
	var m myWriterCloser
	c := NewExportMetricsServiceRequestWriterCloser(&m, log.NewNopLogger())
	defer c.Close()
	req := &metricsService.ExportMetricsServiceRequest{
		ResourceMetrics: []*metric_pb.ResourceMetrics{
			&metric_pb.ResourceMetrics{},
		},
	}
	if err := c.Store(req); err != nil {
		t.Fatal(err)
	}

	storedReq := &metricsService.ExportMetricsServiceRequest{}
	err := proto.Unmarshal(m.Buffer.Bytes(), storedReq)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(req, storedReq) {
		t.Errorf("Expect requests as %v, but stored as: %v", req, storedReq)
	}
}
