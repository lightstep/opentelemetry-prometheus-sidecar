# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

This project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

## [0.9.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.9.0) - 2021-01-15

### Changed

- Additional sanitization of HTTP2 headers. (#67)
- Remove gRPC WithLoadBalancerName() option in favor of default service config. (#64)

## [0.8.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.8.0) - 2021-01-11

### Changed

- Improve startup diagnostics, add selftest step to check for a healthy endpoint. (#62)
- Remove github.com/mwitkow/go-conntrack depdendency (wasn't used). (#63)
- Replace oklog/oklog/pkg/group w/ up-to-date oklog/run dependency. (#63)
- Avoid setting non-nil connection value after DialContext() failure. (#63)
- Isolate telemetry-related code into separate module, create stand-alone telemetry test. (#63)
- Use a gRPC default service config (copied from OTel-Go OTLP gRPC Exporter). (#63)

## [0.7.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.7.0) - 2020-12-24

### Changed

- Trim whitespace in configured HTTP headers and OpenTelemetry attributes.  Avoid starting with newlines embedded in these strings. (#61)

## [0.6.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.6.0) - 2020-12-16

### Changed

- gRPC logging is enabled only when --log.level=debug or --log.verbose > 0. (#59)
- Updated Kubernetes Helm chart example, removed former `kube` sub-directory. (#60)

## [0.5.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.5.0) - 2020-12-12

### Changed

- Update to gRPC v1.34.0 (#54)
- Update to OTel-Go v0.15. (#56)

## [0.4.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.4.0) - 2020-12-09

### Changed

- Add `--log.verbose` setting and enable verbose gRPC logging. (#50)
- Add `--destination.timeout` and `--diagnostics.timeout` values to set gRPC timeout for
  primary and diagnostic OTLP Export() calls. (#51)

## [0.3.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.3.0) - 2020-12-08

- Change several metric names to use `.` instead of `_` for 
  OpenTelemetry consistency. (#43)
- Update to OpenTelemetry-Go SDK version 0.14. (#41)
- Removed unnecessary code that reduced batching capability. (#45)
- Truncate server error messages to 512 bytes. (#46)
- Implement `--prometheus.max-point-age` flag, default 25h. (#47)

## [0.2.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.2.0) - 2020-11-20

### Added

- Support for all settings is available through the YAML configuration file.
  Formerly the configuration file was limited to `metric_renames` and
  `static_metadata` settings.  Command-line flag values override their equivalent
  configuration struct fields.
- The OpenTelemetry-Go SDK is being used with HTTP and gRPC tracing,
  runtime and host metrics instrumentation packages.
- Testing for the [example YAML configuration](sidecar.example).

### Changed

- Prometheus library dependency set to v2.22.0 release (Oct 15, 2020),
  removes the legacy `prometheus/tsdb` dependency.
- Many command-line flags names were changed so that command-line names
  match the configuration file structure.  Please review the [up-to-date
  documentation](README.md#configuration).
- The `metrics_prefix` functionality no longer insers a `/` between a
  non-empty prefix and the metric name.

### Removed

- Existing Prometheus client and OpenCensus instrumentation was removed.

### Fixed

- `tail/tail.go` has an updated copy of `listSegments()` from Prometheus
  v2.22.0.

## [0.1.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.1.0) - 2020-10-21

### Added

- See the [initial release description](./README.md#changes-relative-to-stackdriver).
