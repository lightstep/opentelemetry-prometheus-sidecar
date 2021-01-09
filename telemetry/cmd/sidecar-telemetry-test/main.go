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

	"github.com/lightstep/opentelemetry-prometheus-sidecar/telemetry"
	"go.opentelemetry.io/otel"
)

var logger = telemetry.DefaultLogger()

func main() {
	if err := Main(); err != nil {
		// @@@ LOG NOT WORKING: use logger after fix
		log.Printf("%s: %s\n", os.Args[0], err)
		fmt.Printf("%s: %s\n", os.Args[0], err)
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
		telemetry.WithExporterEndpoint(address),
		telemetry.WithExporterInsecure(insecure),
		telemetry.WithHeaders(headers),
	).Shutdown(context.Background())

	log.Println("sending OTLP to", endpointURL)
	fmt.Println("sending OTLP to", endpointURL)

	tracer := otel.Tracer("sidecar-telemetry-test")
	for {
		_, span := tracer.Start(context.Background(), "ping")
		defer span.End()

		time.Sleep(time.Second)
	}
}
