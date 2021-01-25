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

package supervisor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/health"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/label"
	"go.opentelemetry.io/otel/semconv"
	"go.opentelemetry.io/otel/trace"
)

var (
	tracer = otel.Tracer("github.com/lightstep/opentelemetry-prometheus-sidecar/supervisor")
)

type (
	Config struct {
		Logger log.Logger
		Admin  config.AdminConfig
	}

	Supervisor struct {
		logger   log.Logger
		endpoint string
		client   *http.Client

		lock      sync.Mutex
		logBuffer []string
	}
)

func New(cfg Config) *Supervisor {
	admin := cfg.Admin
	endpoint := fmt.Sprint("http://", admin.ListenIP, ":", admin.Port, "/-/health")
	client := &http.Client{
		// Note: this connection is not traced, even though we
		// use a span around the healthcheck.
		// Transport: otelhttp.NewTransport(http.DefaultTransport),
	}
	return &Supervisor{
		logger:    cfg.Logger,
		endpoint:  endpoint,
		client:    client,
		logBuffer: make([]string, 0, config.DefaultSupervisorLogsHistory),
	}
}

func (s *Supervisor) Run(args []string) bool {
	if err := s.start(args); err != nil {
		level.Error(s.logger).Log("msg", "sidecar failed", "err", err)
		return false
	}

	return true
}

func (s *Supervisor) start(args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.Command(args[0], args[1:]...)

	stderrPipe := s.newPipe(ctx, "stderr", os.Stderr)
	defer stderrPipe.Close()

	cmd.Stderr = stderrPipe
	cmd.Stdout = os.Stdout // sidecar doesn't use stdout, don't care

	if err := cmd.Start(); err != nil {
		return errors.Wrap(err, "supervisor exec")
	}

	go s.supervise(ctx)

	err := cmd.Wait()

	if err == nil {
		level.Info(s.logger).Log("msg", "exiting")
		return nil
	}

	return err
}

func (s *Supervisor) supervise(ctx context.Context) {
	ticker := time.NewTicker(config.DefaultSupervisorPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.healthcheck(); err != nil {
				level.Error(s.logger).Log("msg", "healthcheck failed", "err", err)
			}
		}
	}
}

func (s *Supervisor) healthcheck() (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultHealthCheckTimeout)
	defer cancel()

	ctx, span := tracer.Start(ctx, "health-client")

	defer func() {
		if err != nil {
			span.RecordError(err)
		}
		span.End()
	}()

	req, err := http.NewRequestWithContext(ctx, "GET", s.endpoint, nil)
	if err != nil {
		return errors.Wrap(err, "build request")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return errors.Wrap(err, "healtcheck GET")
	}
	defer resp.Body.Close()

	var hr health.Response
	if err := json.NewDecoder(resp.Body).Decode(&hr); err != nil {
		return errors.Wrap(err, "decode response")
	}

	span.SetAttributes(append(
		semconv.HTTPAttributesFromHTTPStatusCode(resp.StatusCode),
		label.String("sidecar.health", hr.Status),
	)...)
	span.SetStatus(semconv.SpanStatusFromHTTPStatusCode(resp.StatusCode))

	span.AddEvent("recent-logs", trace.WithAttributes(
		label.Array("activity", s.copyLogs()),
	))
	return nil
}

func (s *Supervisor) newPipe(ctx context.Context, name string, real io.Writer) *io.PipeWriter {
	rd, wr := io.Pipe()

	go s.readOutput(ctx, name, real, rd)

	return wr
}

func (s *Supervisor) readOutput(ctx context.Context, name string, real io.Writer, rd *io.PipeReader) {
	buffer := make([]byte, 16384)
	defer rd.Close()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		nread, err := rd.Read(buffer)

		if err != nil {
			level.Info(s.logger).Log("msg", "read sidecar output", "err", err)
			return
		}

		for writeBuf := buffer[0:nread]; len(writeBuf) != 0; {
			// Ignore the result of writing to the real output stream.
			n, err := real.Write(writeBuf)
			if err != nil {
				level.Info(s.logger).Log("msg", "write supervisor output", "err", err)
				break
			}
			writeBuf = writeBuf[n:]
		}

		scanner := bufio.NewScanner(bytes.NewReader(buffer[0:nread]))
		for scanner.Scan() {
			s.addLogLine(scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			level.Debug(s.logger).Log("msg", "parse error scanning sidecar log", "err", err)
		}
	}
}

func (s *Supervisor) addLogLine(line string) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if len(s.logBuffer) == cap(s.logBuffer) {
		point := cap(s.logBuffer) - cap(s.logBuffer)/2
		copy(s.logBuffer[0:point], s.logBuffer[point:])
		s.logBuffer = s.logBuffer[0:point]
	}

	s.logBuffer = append(s.logBuffer, line)
}

func (s *Supervisor) copyLogs() []string {
	s.lock.Lock()
	defer s.lock.Unlock()

	cp := make([]string, len(s.logBuffer))
	copy(cp, s.logBuffer)
	return cp
}
