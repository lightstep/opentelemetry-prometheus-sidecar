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
	"regexp"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/health"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"github.com/pkg/errors"
	"github.com/prometheus/common/version"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/semconv"
	"go.opentelemetry.io/otel/trace"
)

var (
	tracer = otel.GetTracerProvider().Tracer("github.com/lightstep/opentelemetry-prometheus-sidecar/supervisor", trace.WithInstrumentationVersion(version.Version))

	stackdumpRE = regexp.MustCompile(`goroutine \d+ \[.+\]:\n`)
)

const (
	supervisorBufferSize = 1 << 14
)

type (
	Config struct {
		Logger    log.Logger
		Admin     config.AdminConfig
		Telemetry *telemetry.Telemetry
	}

	Supervisor struct {
		logger   log.Logger
		endpoint string
		client   *http.Client
		telem    *telemetry.Telemetry
		period   time.Duration

		// isRunning means the sidecar enter its run state.
		isRunning bool

		// veryUnhealthy means the sidecar returned a stackdump.
		veryUnhealthy bool

		lock      sync.Mutex
		logBuffer []string
	}
)

func New(cfg Config) *Supervisor {
	admin := cfg.Admin
	endpoint := fmt.Sprint("http://", admin.ListenIP, ":", admin.Port)
	client := &http.Client{
		// Note: this connection is not traced, even though we
		// use a span around the healthcheck.
		// Transport: otelhttp.NewTransport(http.DefaultTransport),
	}
	return &Supervisor{
		logger:    cfg.Logger,
		endpoint:  endpoint,
		period:    cfg.Admin.HealthCheckPeriod.Duration,
		client:    client,
		telem:     cfg.Telemetry,
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
	ctx, cancelMain := telemetry.ContextWithSIGTERM(s.logger)
	defer cancelMain()

	cmd := exec.Command(args[0], args[1:]...)

	var wg sync.WaitGroup

	stderrPipe := s.newPipe(ctx, "stderr", os.Stderr, &wg)
	defer stderrPipe.Close()

	cmd.Stderr = stderrPipe
	cmd.Stdout = os.Stdout // sidecar doesn't use stdout, don't care

	if err := cmd.Start(); err != nil {
		return errors.Wrap(err, "supervisor exec")
	}

	wg.Add(1)
	go s.supervise(ctx, cmd, &wg)
	go func() {
		// As soon as this context is cancelled (possibly by a
		// SIGTERM), deliver SIGTERM to the subordinate
		// process.  The pipe reader below will continue reading
		// until EOF.
		<-ctx.Done()
		cmd.Process.Signal(os.Interrupt)

		// If the subordinate still doesn't exit, force the
		// issue.  The `cmd.Wait()` below otherwise cannot proceed.
		time.Sleep(config.DefaultShutdownDelay)
		cmd.Process.Kill()
	}()

	err := cmd.Wait()

	cancelMain()       // Shutdown the healtchecker via context.
	stderrPipe.Close() // Close the pipe writer, reader reads through EOF.
	wg.Wait()          // Wait for both goroutines.
	s.finalSpan(err)   // Send a final span, includes possible stack dump.

	if err == nil {
		level.Info(s.logger).Log("msg", "sidecar supervisor exiting")
		return nil
	}
	return err
}

func (s *Supervisor) supervise(ctx context.Context, cmd *exec.Cmd, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		var sleep time.Duration
		if s.isRunning {
			sleep = s.period
		} else {
			sleep = config.DefaultReadinessPeriod
		}

		// Sleep or be canceled.
		if func() bool {
			tick := time.NewTicker(sleep)
			defer tick.Stop()
			select {
			case <-ctx.Done():
				return true
			case <-tick.C:
				return false
			}
		}() {
			return
		}

		s.healthcheck(ctx)

		if s.veryUnhealthy {
			// A stackdump was already reported. We're
			// going to flush these spans, stop reporting
			// telemetry, then kill the process.
			s.finalSpan(errors.New("process is unhealthy"))

			s.telem.Shutdown(ctx)

			// The process is repeatedly failing its internal health check,
			// and we have seen a stackdump already.
			cmd.Process.Kill()
			return
		}
	}
}

func (s *Supervisor) healthcheck(ctx context.Context) {
	if err := s.healthcheckErr(ctx); err != nil {
		if err != context.Canceled {
			level.Error(s.logger).Log("msg", "healthcheck failed", "err", err)
		}
	}
}

func (s *Supervisor) healthcheckErr(ctx context.Context) (err error) {
	ctx, cancel := context.WithTimeout(ctx, config.DefaultHealthCheckTimeout)
	defer cancel()

	ctx, span := tracer.Start(ctx, "health-client")

	defer func() {
		if err != nil {
			span.RecordError(err)
		}
		span.End()
	}()

	span.AddEvent("recent-logs", trace.WithAttributes(
		attribute.Array("activity", s.copyLogs()),
	))

	var hr health.Response

	// Make the request and try to parse the result.
	err = func() error {
		req, err := http.NewRequestWithContext(ctx, "GET", s.endpoint+config.HealthCheckURI, nil)
		if err != nil {
			return errors.Wrap(err, "build request")
		}

		resp, err := s.client.Do(req)
		if err != nil {
			return errors.Wrap(err, "healtcheck GET")
		}
		defer resp.Body.Close()

		// Note: The Go SDK 0.16 appears to lose track of the codes.Error status
		// being set here. It does not appear in the output span.  TODO: investigate.
		span.SetAttributes(semconv.HTTPAttributesFromHTTPStatusCode(resp.StatusCode)...)
		span.SetStatus(semconv.SpanStatusFromHTTPStatusCode(resp.StatusCode))

		if err := json.NewDecoder(resp.Body).Decode(&hr); err != nil {
			return errors.Wrap(err, "decode response")
		}
		span.SetAttributes(attribute.String("sidecar.status", hr.Status))

		if hr.Stackdump != "" {
			span.SetAttributes(attribute.String("sidecar.stackdump", hr.Stackdump))
		}

		if resp.StatusCode/100 != 2 {
			return errors.Errorf("healthcheck: %s", hr.Status)
		}

		return nil
	}()

	var hstat string
	if err == nil {
		hstat = s.noteHealthy(hr)
	} else {
		hstat = s.noteUnhealthy(hr)
	}

	span.SetAttributes(attribute.String("sidecar.health", hstat))
	return err
}

func (s *Supervisor) finalSpan(err error) {
	_, span := tracer.Start(context.Background(), "shutdown-report")
	defer span.End()

	span.AddEvent("recent-logs", trace.WithAttributes(
		attribute.Array("activity", s.copyLogs()),
	))

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return
	}
	span.SetStatus(codes.Ok, "graceful shutdown")
}

func (s *Supervisor) newPipe(ctx context.Context, name string, real io.Writer, wg *sync.WaitGroup) *io.PipeWriter {
	wg.Add(1)

	rd, wr := io.Pipe()

	go s.readOutput(ctx, name, real, rd, wg)

	return wr
}

func (s *Supervisor) readOutput(_ context.Context, name string, real io.Writer, rd *io.PipeReader, wg *sync.WaitGroup) {
	// Note: We disregard the context here in order to read and
	// copy the stream until reaching EOF.  The other end of the
	// pipe will close the pipe Writer when the remote process
	// closes the output stream.
	defer wg.Done()

	buffer := make([]byte, supervisorBufferSize)
	defer rd.Close()

	seenStackdump := false
	var stackdump bytes.Buffer
	defer func() {
		if stackdump.Len() > 0 {
			s.addLogLine(stackdump.String())
		}
	}()

	for {
		nread, err := rd.Read(buffer)

		if err != nil {
			if err != io.EOF {
				level.Info(s.logger).Log("msg", "read sidecar output", "err", err)
			}
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

		output := buffer[0:nread]

		if seenStackdump || s.isStackdump(output) {
			seenStackdump = true
			_, _ = stackdump.Write(output)
			continue
		}

		scanner := bufio.NewScanner(bytes.NewReader(output))
		for scanner.Scan() {
			s.addLogLine(scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			level.Info(s.logger).Log("msg", "parse error scanning sidecar log", "err", err)
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

func (s *Supervisor) noteHealthy(hr health.Response) string {
	s.isRunning = hr.Running

	var summary []interface{}
	if len(hr.Metrics) == 0 {
		summary = append(summary, "msg", "sidecar is initializing")
	} else {
		summary = append(summary, "msg", "sidecar is running")

		summary = append(summary, hr.MetricLogSummary(config.ProducedPointsMetric)...)
		summary = append(summary, hr.MetricLogSummary(config.DroppedSeriesMetric)...)
		summary = append(summary, hr.MetricLogSummary(config.DroppedPointsMetric)...)
		summary = append(summary, hr.MetricLogSummary(config.SkippedPointsMetric)...)
		summary = append(summary, hr.MetricLogSummary(config.OutcomeMetric)...)
	}

	level.Info(s.logger).Log(summary...)

	return "ok"
}

func (s *Supervisor) noteUnhealthy(hr health.Response) string {
	if hr.Stackdump != "" {
		s.veryUnhealthy = true

		summary := []interface{}{
			"msg", "process is unhealthy",
			"stackdump", hr.Stackdump,
		}
		level.Warn(s.logger).Log(summary...)
	}
	return "unhealthy"
}

func (s *Supervisor) isStackdump(text []byte) bool {
	return stackdumpRE.Find(text) != nil
}
