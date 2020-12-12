// Copyright 2016 The Prometheus Authors
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

package otlp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	sidecar "github.com/lightstep/opentelemetry-prometheus-sidecar"
	metricsService "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/collector/metrics/v1"
	"github.com/prometheus/common/version"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/balancer/roundrobin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpcMetadata "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	MaxTimeseriesesPerRequest = 200
)

var (
	pointsExported = sidecar.OTelMeterMust.NewInt64Counter(
		"points.exported",
		metric.WithDescription("count of exported metric points"),
	)
)

// Client allows reading and writing from/to a remote gRPC endpoint. The
// implementation may hit a single backend, so the application should create a
// number of these clients.
type Client struct {
	logger           log.Logger
	url              *url.URL
	timeout          time.Duration
	rootCertificates []string
	headers          grpcMetadata.MD

	conn *grpc.ClientConn
}

// ClientConfig configures a Client.
type ClientConfig struct {
	Logger           log.Logger
	URL              *url.URL
	Timeout          time.Duration
	RootCertificates []string
	Headers          grpcMetadata.MD
}

// NewClient creates a new Client.
func NewClient(conf *ClientConfig) *Client {
	logger := conf.Logger
	if logger == nil {
		logger = log.NewNopLogger()
	}
	return &Client{
		logger:           logger,
		url:              conf.URL,
		timeout:          conf.Timeout,
		rootCertificates: conf.RootCertificates,
		headers:          conf.Headers,
	}
}

type recoverableError struct {
	error
}

// version.* is populated for 'promu' builds, so this will look broken in unit tests.
var userAgent = fmt.Sprintf("OpenTelemetryPrometheus/%s", version.Version)

func (c *Client) getConnection(ctx context.Context) (*grpc.ClientConn, error) {
	if c.conn != nil {
		return c.conn, nil
	}

	useAuth := c.url.Scheme == "https"
	level.Debug(c.logger).Log(
		"msg", "new otlp connection",
		"auth", useAuth,
		"url", c.url.String())

	dopts := []grpc.DialOption{
		grpc.WithBalancerName(roundrobin.Name),
		grpc.WithBlock(), // Wait for the connection to be established before using it.
		grpc.WithUserAgent(userAgent),
		grpc.WithUnaryInterceptor(otelgrpc.UnaryClientInterceptor()),
	}
	if useAuth {
		var tcfg tls.Config
		if len(c.rootCertificates) != 0 {
			certPool := x509.NewCertPool()

			for _, cert := range c.rootCertificates {
				bs, err := ioutil.ReadFile(cert)
				if err != nil {
					return nil, fmt.Errorf("could not read certificate authority certificate: %s: %w", cert, err)
				}

				ok := certPool.AppendCertsFromPEM(bs)
				if !ok {
					return nil, fmt.Errorf("could not parse certificate authority certificate: %s: %w", cert, err)
				}
			}

			tcfg = tls.Config{
				ServerName: c.url.Hostname(),
				RootCAs:    certPool,
			}
		}
		dopts = append(dopts, grpc.WithTransportCredentials(credentials.NewTLS(&tcfg)))
	} else {
		dopts = append(dopts, grpc.WithInsecure())
	}
	address := c.url.Hostname()
	if len(c.url.Port()) > 0 {
		address = net.JoinHostPort(address, c.url.Port())
	}
	conn, err := grpc.DialContext(ctx, address, dopts...)
	c.conn = conn
	if err != nil {
		level.Debug(c.logger).Log(
			"msg", "connection status",
			"address", address,
			"err", err,
		)
	}
	if err == context.DeadlineExceeded {
		return conn, recoverableError{err}
	}
	return conn, err
}

// Store sends a batch of samples to the HTTP endpoint.
func (c *Client) Store(req *metricsService.ExportMetricsServiceRequest) error {
	tss := req.ResourceMetrics
	if len(tss) == 0 {
		// Nothing to do, return silently.
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	conn, err := c.getConnection(ctx)
	if err != nil {
		return err
	}

	service := metricsService.NewMetricsServiceClient(conn)

	errors := make(chan error, len(tss)/MaxTimeseriesesPerRequest+1)
	var wg sync.WaitGroup
	for i := 0; i < len(tss); i += MaxTimeseriesesPerRequest {
		end := i + MaxTimeseriesesPerRequest
		if end > len(tss) {
			end = len(tss)
		}
		wg.Add(1)
		go func(begin int, end int) {
			defer wg.Done()
			req_copy := &metricsService.ExportMetricsServiceRequest{
				ResourceMetrics: req.ResourceMetrics[begin:end],
			}
			_, err := service.Export(grpcMetadata.NewOutgoingContext(ctx, c.headers), req_copy)
			if err == nil {
				// Points were successfully written.
				pointsExported.Add(ctx, int64(end-begin))

				level.Debug(c.logger).Log(
					"msg", "Write was successful",
					"records", end-begin)
			} else {
				// TODO This happens too fast _after_ a healthy connection becomes unhealthy. Fix.
				level.Debug(c.logger).Log(
					"msg", "Failure calling Export",
					"err", truncateErrorString(err))
				status, ok := status.FromError(err)
				// TODO metrics
				if !ok {
					level.Warn(c.logger).Log("msg", "Unexpected error message type from OpenTelemetry service", "err", err)
					errors <- err
					return
				}
				switch status.Code() {
				case codes.DeadlineExceeded, codes.Canceled, codes.ResourceExhausted,
					codes.Aborted, codes.OutOfRange, codes.Unavailable, codes.DataLoss:
					// See https://github.com/open-telemetry/opentelemetry-specification/
					// blob/master/specification/protocol/otlp.md#response
					errors <- recoverableError{err}
				default:
					errors <- err
				}
			}
		}(i, end)
	}
	wg.Wait()
	close(errors)
	if err, ok := <-errors; ok {
		return err
	}
	return nil
}

func (c Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
