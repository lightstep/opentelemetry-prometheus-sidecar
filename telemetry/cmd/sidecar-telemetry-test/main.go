package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-kit/kit/log/level"
	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"go.opentelemetry.io/otel"
)

var logger = telemetry.DefaultLogger()

func main() {
	if err := Main(); err != nil {
		level.Error(logger).Log("msg", err)
		os.Exit(1)
	}
}

func Main() error {
	telemetry.StaticSetup(logger)
	telemetry.SetVerboseLevel(99)

	if len(os.Args) < 2 {
		return fmt.Errorf("usage: %s https://ingest.data.com:443 Header=Value ...", os.Args[0])
	}

	endpointURL, err := url.Parse(os.Args[1])
	if err != nil {
		return err
	}

	headers := map[string]string{}
	for _, hdr := range os.Args[2:] {
		kv := strings.SplitN(hdr, "=", 2)
		headers[kv[0]] = kv[1]
	}

	address := endpointURL.Hostname()
	if len(endpointURL.Port()) > 0 {
		address = net.JoinHostPort(address, endpointURL.Port())
	}

	insecure := false
	switch endpointURL.Scheme {
	case "http":
		insecure = true
	case "https":
	default:
		return fmt.Errorf("invalid endpoint, use https:// or http://")
	}

	defer telemetry.ConfigureOpentelemetry(
		telemetry.WithLogger(logger),
		telemetry.WithSpanExporterEndpoint(address),
		telemetry.WithSpanExporterInsecure(insecure),
		telemetry.WithMetricsExporterEndpoint(address),
		telemetry.WithMetricsExporterInsecure(insecure),
		telemetry.WithHeaders(headers),
		telemetry.WithResourceAttributes(map[string]string{
			"service.name": "sidecar-telemetry-test",
		}),
	).Shutdown(context.Background())

	otel.Handle(fmt.Errorf("printing OTel error"))

	log.Print("printing STDLOG error")

	level.Info(logger).Log("msg", "sending OTLP", "endpoint", endpointURL)

	tracer := otel.Tracer("sidecar-telemetry-test")
	for {
		_, span := tracer.Start(context.Background(), "ping")
		time.Sleep(time.Second)
		span.End()
	}
}
