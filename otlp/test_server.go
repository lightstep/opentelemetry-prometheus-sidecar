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
package otlp

import (
	"context"

	metricsService "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/collector/metrics/v1"
	"google.golang.org/grpc/status"
)

type metricServiceServer struct {
	status *status.Status
}

func (s *metricServiceServer) Export(ctx context.Context, req *metricsService.ExportMetricsServiceRequest) (*metricsService.ExportMetricsServiceResponse, error) {
	var emptyValue = metricsService.ExportMetricsServiceResponse{}
	// md, ok := grpcMetadata.FromIncomingContext(ctx)
	if s.status == nil {
		return &emptyValue, nil
	}

	return nil, s.status.Err()
}
