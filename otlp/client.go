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
	"strconv"
	"sync"
	"time"

	metricsService "github.com/lightstep/opentelemetry-prometheus-sidecar/internal/opentelemetry-proto-gen/collector/metrics/v1"
	// "go.opencensus.io/plugin/ocgrpc"
	// "go.opencensus.io/stats"
	// "go.opencensus.io/stats/view"
	// "go.opencensus.io/tag"
	"google.golang.org/grpc"
	"google.golang.org/grpc/balancer/roundrobin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpcMetadata "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/resolver/manual"
	"google.golang.org/grpc/status"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/common/version"
)

const (
	MaxTimeseriesesPerRequest = 200
)

// var (
// 	// StatusTag is the google3 canonical status code: google3/google/rpc/code.proto
// 	StatusTag = tag.MustNewKey("status")

// 	// PointCount is a metric.
// 	PointCount = stats.Int64("agent.googleapis.com/agent/monitoring/point_count",
// 		"count of metric points written to OpenCensus", stats.UnitDimensionless)
// )

// func init() {
// 	if err := view.Register(
// 		&view.View{
// 			Measure:     PointCount,
// 			TagKeys:     []tag.Key{StatusTag},
// 			Aggregation: view.Sum(),
// 		},
// 	); err != nil {
// 		panic(err)
// 	}
// }

// Client allows reading and writing from/to a remote gRPC endpoint. The
// implementation may hit a single backend, so the application should create a
// number of these clients.
type Client struct {
	logger          log.Logger
	url             *url.URL
	timeout         time.Duration
	resolver        *manual.Resolver
	rootCertificate string
	headers         grpcMetadata.MD

	conn *grpc.ClientConn
}

// ClientConfig configures a Client.
type ClientConfig struct {
	Logger          log.Logger
	URL             *url.URL
	Timeout         time.Duration
	Resolver        *manual.Resolver
	RootCertificate string
	Headers         grpcMetadata.MD
}

// NewClient creates a new Client.
func NewClient(conf *ClientConfig) *Client {
	logger := conf.Logger
	if logger == nil {
		logger = log.NewNopLogger()
	}
	return &Client{
		logger:          logger,
		url:             conf.URL,
		timeout:         conf.Timeout,
		resolver:        conf.Resolver,
		rootCertificate: conf.RootCertificate,
		headers:         conf.Headers,
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

	useAuth, err := strconv.ParseBool(c.url.Query().Get("auth"))
	if err != nil {
		useAuth = true // Default to auth enabled.
	}
	level.Debug(c.logger).Log(
		"msg", "new otlp connection",
		"auth", useAuth,
		"url", c.url.String())

	dopts := []grpc.DialOption{
		grpc.WithBalancerName(roundrobin.Name),
		grpc.WithBlock(), // Wait for the connection to be established before using it.
		grpc.WithUserAgent(userAgent),
		//grpc.WithStatsHandler(&ocgrpc.ClientHandler{}),
	}
	if useAuth {
		var tcfg tls.Config
		if c.rootCertificate != "" {
			certPool := x509.NewCertPool()
			bs, err := ioutil.ReadFile(c.rootCertificate)
			if err != nil {
				return nil, fmt.Errorf("could not read certificate authority certificate: %s: %w", c.rootCertificate, err)
			}

			ok := certPool.AppendCertsFromPEM(bs)
			if !ok {
				return nil, fmt.Errorf("could not parse certificate authority certificate: %s: %w", c.rootCertificate, err)
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
	if c.resolver != nil {
		address = c.resolver.Scheme() + ":///" + address
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
				// The response is empty if all points were successfully written.
				// stats.RecordWithTags(ctx,
				// 	[]tag.Mutator{tag.Upsert(StatusTag, "0")},
				// 	PointCount.M(int64(end-begin)))
				level.Debug(c.logger).Log(
					"msg", "Write was successful",
					"records", end-begin)
			} else {
				// TODO This happens too fast _after_ a healthy connection becomes unhealthy. Fix.
				level.Debug(c.logger).Log(
					"msg", "Partial failure calling Export",
					"err", err)
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
