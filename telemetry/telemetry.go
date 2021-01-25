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
	"github.com/lightstep/opentelemetry-prometheus-sidecar/config"
	"github.com/pkg/errors"
	hostMetrics "go.opentelemetry.io/contrib/instrumentation/host"
	runtimeMetrics "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/contrib/propagators/b3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp"
	"go.opentelemetry.io/otel/label"
	"go.opentelemetry.io/otel/propagation"
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
		shutdownFuncs []func(context.Context) error
	}

	Option func(*Config)

	Config struct {
		SpanExporterEndpoint            string
		SpanExporterEndpointInsecure    bool
		MetricsExporterEndpoint         string
		MetricsExporterEndpointInsecure bool

		Propagators           []string
		MetricReportingPeriod time.Duration
		ResourceAttributes    map[string]string
		Headers               map[string]string
		ExportTimeout         time.Duration
		resource              *resource.Resource
		logger                log.Logger
	}

	setupFunc func() (start, stop func(context.Context) error, err error)
)

// WithSpanExporterEndpoint configures the endpoint for sending spans via OTLP
func WithSpanExporterEndpoint(url string) Option {
	return func(c *Config) {
		c.SpanExporterEndpoint = url
	}
}

// WithSpanExporterInsecure permits connecting to the
// trace endpoint without a certificate
func WithSpanExporterInsecure(insecure bool) Option {
	return func(c *Config) {
		c.SpanExporterEndpointInsecure = insecure
	}
}

// WithMetricsExporterEndpoint configures the endpoint for sending metricss via OTLP
func WithMetricsExporterEndpoint(url string) Option {
	return func(c *Config) {
		c.MetricsExporterEndpoint = url
	}
}

// WithMetricsExporterInsecure permits connecting to the
// trace endpoint without a certificate
func WithMetricsExporterInsecure(insecure bool) Option {
	return func(c *Config) {
		c.MetricsExporterEndpointInsecure = insecure
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
		c.MetricReportingPeriod = p
	}
}

// WithExportTimeout configures the timeout used for Export().
func WithExportTimeout(t time.Duration) Option {
	return func(c *Config) {
		c.ExportTimeout = t
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

func DefaultLogger(opts ...level.Option) log.Logger {
	if opts == nil {
		opts = append(opts, level.AllowAll())
	}
	logWriter := log.NewLogfmtLogger(log.NewSyncWriter(os.Stdout))
	return log.With(level.NewFilter(logWriter, opts...),
		"ts", log.TimestampFormat(
			func() time.Time { return time.Now().UTC() },
			"2006-01-02T15:04:05.000Z07:00",
		))
}

func newConfig(opts ...Option) Config {
	var c Config
	c.Propagators = []string{"b3"}
	c.logger = DefaultLogger(level.AllowInfo())

	var defaultOpts []Option

	for _, opt := range append(defaultOpts, opts...) {
		opt(&c)
	}

	if c.ExportTimeout <= 0 {
		c.ExportTimeout = config.DefaultExportTimeout
	}
	if c.MetricReportingPeriod <= 0 {
		c.MetricReportingPeriod = config.DefaultReportingPeriod
	}

	var err error
	c.resource, err = newResource(&c)
	if err != nil {
		c.logger.Log("msg", "telemtry resource initialization failed", "error", err)
	}
	return c
}

// configurePropagators configures B3 propagation by default
func configurePropagators(c *Config) error {
	propagatorsMap := map[string]propagation.TextMapPropagator{
		"b3":           b3.B3{},
		"baggage":      propagation.Baggage{},
		"tracecontext": propagation.TraceContext{},
	}
	var props []propagation.TextMapPropagator
	for _, key := range c.Propagators {
		prop := propagatorsMap[key]
		if prop != nil {
			props = append(props, prop)
		}
	}
	if len(props) == 0 {
		return fmt.Errorf("invalid configuration: unsupported propagators. Supported options: b3,cc")
	}
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		props...,
	))
	return nil
}

func newResource(c *Config) (*resource.Resource, error) {
	var kv []label.KeyValue
	for k, v := range c.ResourceAttributes {
		kv = append(kv, label.String(k, v))
	}
	return resource.New(
		context.Background(),
		resource.WithAttributes(kv...),
	)
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

func (c *Config) setupTracing() (start, stop func(ctx context.Context) error, err error) {
	if c.SpanExporterEndpoint == "" {
		level.Debug(c.logger).Log("msg", "tracing is disabled: no endpoint set")
		return nil, nil, nil
	}
	spanExporter := newExporter(c.SpanExporterEndpoint, c.SpanExporterEndpointInsecure, c.Headers)

	// TODO: Make a way to set the export timeout, there is
	// apparently not such a thing for OTel-Go:
	// https://github.com/open-telemetry/opentelemetry-go/issues/1386
	tp := trace.NewTracerProvider(
		trace.WithConfig(trace.Config{DefaultSampler: trace.AlwaysSample()}),
		trace.WithSyncer(spanExporter),
		trace.WithResource(c.resource),
	)

	if err := configurePropagators(c); err != nil {
		return nil, nil, errors.Wrap(err, "failed to configure propagators")
	}

	otel.SetTracerProvider(tp)

	return func(ctx context.Context) error {
			return spanExporter.Start(ctx)
		}, func(ctx context.Context) error {
			return spanExporter.Shutdown(ctx)
		}, nil
}

func (c *Config) setupMetrics() (start, stop func(ctx context.Context) error, err error) {
	if c.MetricsExporterEndpoint == "" {
		level.Debug(c.logger).Log("msg", "metrics are disabled: no endpoint set")
		return nil, nil, nil
	}
	metricExporter := newExporter(c.MetricsExporterEndpoint, c.MetricsExporterEndpointInsecure, c.Headers)

	pusher := controller.New(
		processor.New(
			selector.NewWithInexpensiveDistribution(),
			metricExporter,
		),
		metricExporter,
		controller.WithResource(c.resource),
		controller.WithPeriod(c.MetricReportingPeriod),
		controller.WithTimeout(c.ExportTimeout),
	)

	provider := pusher.MeterProvider()

	otel.SetMeterProvider(provider)

	return func(ctx context.Context) error {
			if err := metricExporter.Start(ctx); err != nil {
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
		}, func(ctx context.Context) error {
			pusher.Stop()
			return metricExporter.Shutdown(ctx)
		}, nil
}

func ConfigureOpentelemetry(opts ...Option) *Telemetry {
	tel := Telemetry{
		config: newConfig(opts...),
	}

	s, _ := json.MarshalIndent(tel.config, "", "\t")
	level.Debug(tel.config.logger).Log("msg", "telemetry enabled", "cfg", string(s))

	var startFuncs []func(context.Context) error

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
		if err := start(context.Background()); err != nil {
			level.Error(tel.config.logger).Log("start error", err)
		}
	}
	return &tel
}

func (tel *Telemetry) Shutdown(ctx context.Context) {
	for _, shutdown := range tel.shutdownFuncs {
		if err := shutdown(ctx); err != nil {
			level.Error(tel.config.logger).Log("msg", "failed to stop exporter", "error", err)
		}
	}
}
