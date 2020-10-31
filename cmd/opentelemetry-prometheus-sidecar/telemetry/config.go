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
	"log"
	"time"

	"github.com/sethvargo/go-envconfig"
	hostMetrics "go.opentelemetry.io/contrib/instrumentation/host"
	runtimeMetrics "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/contrib/propagators/b3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/exporters/otlp"
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

// WithLogLevel configures the logging level for OpenTelemetry
func WithLogLevel(loglevel string) Option {
	return func(c *Config) {
		c.LogLevel = loglevel
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
		c.resourceAttributes = attributes
	}
}

// WithPropagators configures propagators
func WithPropagators(propagators []string) Option {
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

type Logger interface {
	Fatalf(format string, v ...interface{})
	Debugf(format string, v ...interface{})
}

func WithLogger(logger Logger) Option {
	return func(c *Config) {
		c.logger = logger
	}
}

type DefaultLogger struct {
}

func (l *DefaultLogger) Fatalf(format string, v ...interface{}) {
	log.Fatalf(format, v...)
}

func (l *DefaultLogger) Debugf(format string, v ...interface{}) {
	log.Printf(format, v...)
}

type defaultHandler struct {
	logger Logger
}

func (l *defaultHandler) Handle(err error) {
	l.logger.Debugf("error: %v\n", err)
}

const (
	// Note: these values should match the defaults used in `env` tags for Config fields.
	// Note: the MetricExporterEndpoint currently defaults to "".  When LS is ready for OTLP metrics
	// we'll set this to `DefaultMetricExporterEndpoint`.

	DefaultSpanExporterEndpoint   = "ingest.lightstep.com:443"
	DefaultMetricExporterEndpoint = "ingest.lightstep.com:443"
)

type Config struct {
	SpanExporterEndpoint           string   `env:"OTEL_EXPORTER_OTLP_SPAN_ENDPOINT,default=ingest.lightstep.com:443"`
	SpanExporterEndpointInsecure   bool     `env:"OTEL_EXPORTER_OTLP_SPAN_INSECURE,default=false"`
	MetricExporterEndpoint         string   `env:"OTEL_EXPORTER_OTLP_METRIC_ENDPOINT"`
	MetricExporterEndpointInsecure bool     `env:"OTEL_EXPORTER_OTLP_METRIC_INSECURE,default=false"`
	LogLevel                       string   `env:"OTEL_LOG_LEVEL,default=info"`
	Propagators                    []string `env:"OTEL_PROPAGATORS,default=b3"`
	MetricReportingPeriod          string   `env:"OTEL_EXPORTER_OTLP_METRIC_PERIOD,default=30s"`
	resourceAttributes             map[string]string
	Resource                       *resource.Resource
	logger                         Logger
	errorHandler                   otel.ErrorHandler
}

func validateConfiguration(c Config) error {
	return nil
}

func newConfig(opts ...Option) Config {
	var c Config
	envError := envconfig.Process(context.Background(), &c)
	c.logger = &DefaultLogger{}
	c.errorHandler = &defaultHandler{logger: c.logger}
	var defaultOpts []Option

	for _, opt := range append(defaultOpts, opts...) {
		opt(&c)
	}
	c.Resource = newResource(&c)

	if envError != nil {
		c.logger.Fatalf("environment error: %v", envError)
	}

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
	return resource.Empty()
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
		c.logger.Debugf("tracing is disabled by configuration: no endpoint set")
		return nil, nil
	}
	spanExporter, err := newExporter(c.SpanExporterEndpoint, c.SpanExporterEndpointInsecure)
	if err != nil {
		return nil, fmt.Errorf("failed to create span exporter: %v", err)
	}

	tp := trace.NewTracerProvider(
		trace.WithConfig(trace.Config{DefaultSampler: trace.AlwaysSample()}),
		trace.WithSyncer(spanExporter),
		trace.WithResource(c.Resource),
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
		c.logger.Debugf("metrics are disabled by configuration: no endpoint set")
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
		controller.WithResource(c.Resource),
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
	c := newConfig(opts...)

	if c.LogLevel == "debug" {
		c.logger.Debugf("debug logging enabled")
		c.logger.Debugf("configuration")
		s, _ := json.MarshalIndent(c, "", "\t")
		c.logger.Debugf(string(s))
	}

	err := validateConfiguration(c)
	if err != nil {
		c.logger.Fatalf("configuration error: %v", err)
	}

	if c.errorHandler != nil {
		global.SetErrorHandler(c.errorHandler)
	}

	ls := Launcher{
		config: c,
	}
	for _, setup := range []setupFunc{setupTracing, setupMetrics} {
		shutdown, err := setup(c)
		if err != nil {
			c.logger.Fatalf("setup error: %v", err)
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
			ls.config.logger.Fatalf("failed to stop exporter: %v", err)
		}
	}
}
