// Copyright Lightstep Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
	hostMetrics "go.opentelemetry.io/contrib/instrumentation/host"
	runtimeMetrics "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/contrib/propagators/b3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/exporters/otlp"
	"go.opentelemetry.io/otel/label"
	"go.opentelemetry.io/otel/propagators"
	controller "go.opentelemetry.io/otel/sdk/metric/controller/push"
	processor "go.opentelemetry.io/otel/sdk/metric/processor/basic"
	selector "go.opentelemetry.io/otel/sdk/metric/selector/simple"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc/credentials"
)

type (
	Telemetry struct {
		config        Config
		shutdownFuncs []func() error
	}

	Option func(*Config)

	Config struct {
		ExporterEndpoint         string
		ExporterEndpointInsecure bool
		Propagators              []string
		MetricReportingPeriod    string
		ResourceAttributes       map[string]string
		Headers                  map[string]string
		resource                 *resource.Resource
		logger                   log.Logger
	}

	setupFunc func() (start, stop func() error, err error)
)

// WithExporterEndpoint configures the endpoint for sending metrics via OTLP
func WithExporterEndpoint(url string) Option {
	return func(c *Config) {
		c.ExporterEndpoint = url
	}
}

// WithExporterInsecure permits connecting to the
// trace endpoint without a certificate
func WithExporterInsecure(insecure bool) Option {
	return func(c *Config) {
		c.ExporterEndpointInsecure = insecure
	}
}

// WithResourceAttributes configures attributes on the resource
func WithResourceAttributes(attributes map[string]string) Option {
	return func(c *Config) {
		c.ResourceAttributes = attributes
	}
}

// WithPropagators configures propagators
func WithPropagators(propagators ...string) Option {
	return func(c *Config) {
		c.Propagators = propagators
	}
}

// WithMetricReportingPeriod configures the metric reporting period,
// how often the controller collects and exports metric data.
func WithMetricReportingPeriod(p time.Duration) Option {
	return func(c *Config) {
		c.MetricReportingPeriod = fmt.Sprint(p)
	}
}

func WithLogger(logger log.Logger) Option {
	return func(c *Config) {
		c.logger = logger
	}
}

func WithHeaders(headers map[string]string) Option {
	return func(c *Config) {
		c.Headers = headers
	}
}

func newConfig(opts ...Option) Config {
	var c Config
	logWriter := log.NewLogfmtLogger(log.NewSyncWriter(os.Stdout))
	c.Propagators = []string{"b3"}
	c.logger = level.NewFilter(logWriter, level.AllowInfo())

	var defaultOpts []Option

	for _, opt := range append(defaultOpts, opts...) {
		opt(&c)
	}
	c.resource = newResource(&c)
	return c
}

// configurePropagators configures B3 propagation by default
func configurePropagators(c *Config) error {
	propagatorsMap := map[string]otel.TextMapPropagator{
		"b3": b3.B3{},
		"cc": propagators.Baggage{},
	}
	var props []otel.TextMapPropagator
	for _, key := range c.Propagators {
		prop := propagatorsMap[key]
		if prop != nil {
			props = append(props, prop)
		}
	}
	if len(props) == 0 {
		return fmt.Errorf("invalid configuration: unsupported propagators. Supported options: b3,cc")
	}
	global.SetTextMapPropagator(otel.NewCompositeTextMapPropagator(
		props...,
	))
	return nil
}

func newResource(c *Config) *resource.Resource {
	// TODO placholder until
	// https://github.com/open-telemetry/opentelemetry-go/pull/1235
	var kv []label.KeyValue
	for k, v := range c.ResourceAttributes {
		kv = append(kv, label.String(k, v))
	}
	return resource.New(kv...)
}

func newExporter(endpoint string, insecure bool, headers map[string]string) *otlp.Exporter {
	secureOption := otlp.WithTLSCredentials(credentials.NewClientTLSFromCert(nil, ""))
	if insecure {
		secureOption = otlp.WithInsecure()
	}
	return otlp.NewUnstartedExporter(
		secureOption,
		otlp.WithAddress(endpoint),
		otlp.WithHeaders(headers),
	)
}

func (c *Config) setupTracing() (start, stop func() error, err error) {
	if c.ExporterEndpoint == "" {
		level.Debug(c.logger).Log("msg", "tracing is disabled: no endpoint set")
		return nil, nil, nil
	}
	spanExporter := newExporter(c.ExporterEndpoint, c.ExporterEndpointInsecure, c.Headers)

	tp := trace.NewTracerProvider(
		trace.WithConfig(trace.Config{DefaultSampler: trace.AlwaysSample()}),
		trace.WithSyncer(spanExporter),
		trace.WithResource(c.resource),
	)

	if err := configurePropagators(c); err != nil {
		return nil, nil, errors.Wrap(err, "failed to configure propagators")
	}

	global.SetTracerProvider(tp)

	return func() error {
			return spanExporter.Start()
		}, func() error {
			return spanExporter.Shutdown(context.Background())
		}, nil
}

func (c *Config) setupMetrics() (func() error, func() error, error) {
	if c.ExporterEndpoint == "" {
		level.Debug(c.logger).Log("msg", "metrics are disabled: no endpoint set")
		return nil, nil, nil
	}
	metricExporter := newExporter(c.ExporterEndpoint, c.ExporterEndpointInsecure, c.Headers)

	period := controller.DefaultPushPeriod
	if c.MetricReportingPeriod != "" {
		var err error
		period, err = time.ParseDuration(c.MetricReportingPeriod)
		if err != nil {
			return nil, nil, errors.Wrap(err, "invalid metric reporting period")
		}
		if period <= 0 {
			return nil, nil, fmt.Errorf("invalid metric reporting period")
		}
	}
	pusher := controller.New(
		processor.New(
			selector.NewWithInexpensiveDistribution(),
			metricExporter,
		),
		metricExporter,
		controller.WithResource(c.resource),
		controller.WithPeriod(period),
	)

	provider := pusher.MeterProvider()

	global.SetMeterProvider(provider)

	return func() error {
			if err := metricExporter.Start(); err != nil {
				return errors.Wrap(err, "failed to start OTLP exporter")
			}

			if err := runtimeMetrics.Start(runtimeMetrics.WithMeterProvider(provider)); err != nil {
				return errors.Wrap(err, "failed to start runtime metrics")
			}

			if err := hostMetrics.Start(hostMetrics.WithMeterProvider(provider)); err != nil {
				return errors.Wrap(err, "failed to start host metrics")
			}

			pusher.Start()

			return nil
		}, func() error {
			pusher.Stop()
			return metricExporter.Shutdown(context.Background())
		}, nil
}

func ConfigureOpentelemetry(opts ...Option) *Telemetry {
	tel := Telemetry{
		config: newConfig(opts...),
	}

	level.Debug(tel.config.logger).Log("msg", "debug logging enabled")
	s, _ := json.MarshalIndent(tel.config, "", "\t")
	level.Debug(tel.config.logger).Log("configuration", string(s))

	var startFuncs []func() error

	staticSetup(tel.config.logger)

	for _, setup := range []setupFunc{tel.config.setupTracing, tel.config.setupMetrics} {
		start, shutdown, err := setup()
		if err != nil {
			level.Error(tel.config.logger).Log("setup error", err)
			continue
		}
		if shutdown != nil {
			tel.shutdownFuncs = append(tel.shutdownFuncs, shutdown)
		}
		if start != nil {
			startFuncs = append(startFuncs, start)
		}
	}
	for _, start := range startFuncs {
		if err := start(); err != nil {
			level.Error(tel.config.logger).Log("start error", err)
		}
	}
	return &tel
}

func (tel *Telemetry) Shutdown() {
	for _, shutdown := range tel.shutdownFuncs {
		if err := shutdown(); err != nil {
			level.Error(tel.config.logger).Log("msg", "failed to stop exporter", "error", err)
		}
	}
}
