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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/health"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/label"
	"go.opentelemetry.io/otel/semconv"
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
		logger:   cfg.Logger,
		endpoint: endpoint,
		client:   client,
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

	// TODO here tee and capture the output, use it.

	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout

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

	return nil
}
