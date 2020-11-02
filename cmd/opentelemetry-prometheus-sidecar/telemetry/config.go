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

	kitlog "github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
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

type Option func(*Config)

// WithMetricExporterEndpoint configures the endpoint for sending metrics via OTLP
func WithMetricExporterEndpoint(url string) Option {
	return func(c *Config) {
		c.MetricExporterEndpoint = url
	}
}

// WithSpanExporterEndpoint configures the endpoint for sending traces via OTLP
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

// WithMetricExporterInsecure permits connecting to the
// metric endpoint without a certificate
func WithMetricExporterInsecure(insecure bool) Option {
	return func(c *Config) {
		c.MetricExporterEndpointInsecure = insecure
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

// Configures a global error handler to be used throughout an OpenTelemetry instrumented project.
// See "go.opentelemetry.io/otel/api/global"
func WithErrorHandler(handler otel.ErrorHandler) Option {
	return func(c *Config) {
		c.errorHandler = handler
	}
}

// WithMetricReportingPeriod configures the metric reporting period,
// how often the controller collects and exports metric data.
func WithMetricReportingPeriod(p time.Duration) Option {
	return func(c *Config) {
		c.MetricReportingPeriod = fmt.Sprint(p)
	}
}

func WithLogger(logger kitlog.Logger) Option {
	return func(c *Config) {
		c.logger = logger
	}
}

type defaultHandler struct {
	logger kitlog.Logger
}

func (l *defaultHandler) Handle(err error) {
	l.logger.Log("error", err)
}

type Config struct {
	SpanExporterEndpoint           string
	SpanExporterEndpointInsecure   bool
	MetricExporterEndpoint         string
	MetricExporterEndpointInsecure bool
	Propagators                    []string
	MetricReportingPeriod          string
	ResourceAttributes             map[string]string
	resource                       *resource.Resource
	logger                         kitlog.Logger
	errorHandler                   otel.ErrorHandler
}

func validateConfiguration(c Config) error {
	return nil
}

func newConfig(opts ...Option) Config {
	var c Config
	logWriter := kitlog.NewLogfmtLogger(kitlog.NewSyncWriter(os.Stdout))
	c.Propagators = []string{"b3"}
	c.logger = level.NewFilter(kitlog.With(logWriter, "component", "telemetry"), level.AllowInfo())
	c.errorHandler = &defaultHandler{logger: c.logger}
	var defaultOpts []Option

	for _, opt := range append(defaultOpts, opts...) {
		opt(&c)
	}
	c.resource = newResource(&c)
	return c
}

type Launcher struct {
	config        Config
	shutdownFuncs []func() error
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

func newExporter(endpoint string, insecure bool) (*otlp.Exporter, error) {
	headers := map[string]string{}

	secureOption := otlp.WithTLSCredentials(credentials.NewClientTLSFromCert(nil, ""))
	if insecure {
		secureOption = otlp.WithInsecure()
	}
	return otlp.NewExporter(
		secureOption,
		otlp.WithAddress(endpoint),
		otlp.WithHeaders(headers),
	)
}

func setupTracing(c Config) (func() error, error) {
	if c.SpanExporterEndpoint == "" {
		level.Debug(c.logger).Log("msg", "tracing is disabled by configuration: no endpoint set")
		return nil, nil
	}
	spanExporter, err := newExporter(c.SpanExporterEndpoint, c.SpanExporterEndpointInsecure)
	if err != nil {
		return nil, fmt.Errorf("failed to create span exporter: %v", err)
	}

	tp := trace.NewTracerProvider(
		trace.WithConfig(trace.Config{DefaultSampler: trace.AlwaysSample()}),
		trace.WithSyncer(spanExporter),
		trace.WithResource(c.resource),
	)

	if err = configurePropagators(&c); err != nil {
		return nil, err
	}

	global.SetTracerProvider(tp)

	return func() error {
		return spanExporter.Shutdown(context.Background())
	}, nil
}

type setupFunc func(Config) (func() error, error)

func setupMetrics(c Config) (func() error, error) {
	if c.MetricExporterEndpoint == "" {
		level.Debug(c.logger).Log("msg", "metrics are disabled by configuration: no endpoint set")
		return nil, nil
	}
	metricExporter, err := newExporter(c.MetricExporterEndpoint, c.MetricExporterEndpointInsecure)
	if err != nil {
		return nil, fmt.Errorf("failed to create metric exporter: %v", err)
	}

	period := controller.DefaultPushPeriod
	if c.MetricReportingPeriod != "" {
		period, err = time.ParseDuration(c.MetricReportingPeriod)
		if err != nil {
			return nil, fmt.Errorf("invalid metric reporting period: %v", err)
		}
		if period <= 0 {

			return nil, fmt.Errorf("invalid metric reporting period: %v", c.MetricReportingPeriod)
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

	pusher.Start()

	provider := pusher.MeterProvider()

	if err = runtimeMetrics.Start(runtimeMetrics.WithMeterProvider(provider)); err != nil {
		return nil, fmt.Errorf("failed to start runtime metrics: %v", err)
	}

	if err = hostMetrics.Start(hostMetrics.WithMeterProvider(provider)); err != nil {
		return nil, fmt.Errorf("failed to start host metrics: %v", err)
	}

	global.SetMeterProvider(provider)
	return func() error {
		pusher.Stop()
		return metricExporter.Shutdown(context.Background())
	}, nil
}

func ConfigureOpentelemetry(opts ...Option) Launcher {
	// @@@ TODO: See how promlog is configured w/ kingpin
	// flags. Use that mechanism for the config fields in this
	// package where `envconfig` was removed here.
	// Lost a log level flag.... @@@n
	c := newConfig(opts...)

	level.Debug(c.logger).Log("msg", "debug logging enabled")
	s, _ := json.MarshalIndent(c, "", "\t")
	level.Debug(c.logger).Log("configuration", string(s))

	if c.errorHandler != nil {
		global.SetErrorHandler(c.errorHandler)
	}

	ls := Launcher{
		config: c,
	}
	for _, setup := range []setupFunc{setupTracing, setupMetrics} {
		shutdown, err := setup(c)
		if err != nil {
			level.Error(c.logger).Log("setup error", err)
			continue
		}
		if shutdown != nil {
			ls.shutdownFuncs = append(ls.shutdownFuncs, shutdown)
		}
	}
	return ls
}

func (ls Launcher) Shutdown() {
	for _, shutdown := range ls.shutdownFuncs {
		if err := shutdown(); err != nil {
			level.Error(ls.config.logger).Log("msg", "failed to stop exporter", "error", err)
		}
	}
}
