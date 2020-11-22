# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

This project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Added

### Changed

- Change several metric names to use `.` instead of `_` for 
  OpenTelemetry consistency.

### Removed

### Fixed

## [0.2.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.2.0) - 2020-10-20

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

## [0.1.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.1.0) - 2020-10-08

### Added

- See the [initial release description](./README.md#changes-relative-to-stackdriver).
