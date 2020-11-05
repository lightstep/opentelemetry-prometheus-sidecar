This package was copied from Lightstep's [OpenTelemetry Go
Launcher](https://github.com/lightstep/otel-launcher-go).  It was
modified as follows:

- Use of `go-kit/log` logging API for consistency with this code base
- Remove the use of environment variables, as this code base prefers configuration files
- Remove Lightstep-specific functionality
- Standard log package and gRPC logging integration.
