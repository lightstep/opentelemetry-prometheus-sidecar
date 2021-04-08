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
	"io"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/golang/protobuf/proto"
	metricsService "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
)

// ExportMetricsRequestWriterCloser allows writing protobuf message
// monitoring.ExportMetricsRequest as wire format into the writerCloser.
type ExportMetricsRequestWriterCloser struct {
	logger      log.Logger
	writeCloser io.WriteCloser
}

func NewExportMetricsServiceRequestWriterCloser(writeCloser io.WriteCloser, logger log.Logger) *ExportMetricsRequestWriterCloser {
	if logger == nil {
		logger = log.NewNopLogger()
	}
	return &ExportMetricsRequestWriterCloser{
		writeCloser: writeCloser,
		logger:      logger,
	}
}

// Store writes protobuf message monitoring.ExportMetricsRequest as wire
// format into the writeCloser.
func (c *ExportMetricsRequestWriterCloser) Store(req *metricsService.ExportMetricsServiceRequest) error {
	data, err := proto.Marshal(req)
	if err != nil {
		level.Warn(c.logger).Log(
			"msg", "failure marshaling ExportMetricsRequest.",
			"err", err)
		return err
	}
	_, err = c.writeCloser.Write(data)
	if err != nil {
		level.Warn(c.logger).Log(
			"msg", "failure writing data to file.",
			"err", err)
		return err
	}
	return nil
}

func (c *ExportMetricsRequestWriterCloser) Close() error {
	return c.writeCloser.Close()
}
