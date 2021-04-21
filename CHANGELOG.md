# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

This project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Changed

- Performance: do not require in-order writes, use random load balancing. (#198)
- Observability: new metrics `sidecar.refs.collected` and `sidecar.refs.notfound` count series references removed in garbage collection and not found during lookup. (#203)
- Added a loop at the start of `Tail` to wait in the event that prometheus is writing a new checkpoint. The max period for this loop is 5m0s (#205)

## [0.22.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.22.0) - 2021-04-16

### Added

- New metric `sidecar.points.produced` counts total points produced from the WAL. (#187)
- `sidecar.metrics.failing` includes explanation for all unreported metrics, labeled by `key_reason` and `metric_name`, now covers filtered points, metadata errors, and explanations from the server. (#191)

### Changed

- OTLP data points re-use Resource and InstrumentationLibrary (thus are smaller). (#182)
- `sidecar.metrics.invalid` broadened to include non-validation failures, renamed `sidecar.metrics.failing`. (#188)
- Counter reset events output zero values at the reset timestamp, instead of skipping points. (#190)

### Removed

- Removed counters `sidecar.samples.produced` & `sidecar.samples.processed`. (#187)
- Removed counter `sidecar.cumulative.missing_resets`. (#190)
- Removed overlap detection, this cannot happen without the MonitoredResource transform removed in #2. (#190)

## [0.21.1](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.21.1) - 2021-04-06

### Removed

- Removed healthcheck metrics from telemetry traces. (#184)

## [0.21.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.21.0) - 2021-04-06

### Added

- New metric `sidecar.points.skipped` counts points that were not processed due to filters or cumulative resets. (#174)
- New metric `sidecar.series.defined` counts the number of series refs defined in the WAL. (#174)
- New metric `sidecar.metadata.lookups` counts the number of metadata lookups (with error=true/false). (#174)
- New metric `sidecar.cumulative.missing_resets` counts the number of points that were not processed due to cumulative resets. (#174)
- New metric `sidecar.series.current` reports the current number of series (with status=live/filtered/invalid). (#174)
- Added support for handling relabeling rules for `instance` label. (#175)

### Changed

- Series cache remembers points that were filtered in order to correctly count points that are dropped. (#174)
- Metric `sidecar.metadata.fetch.duration` has new `mode` label for single and batch requests. (#174)
- Noisy logs are reduced to emitting once per minute. (#181)

## [0.20.1](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.20.1) - 2021-03-23

### Changed
- The sidecar will no longer wait indefinitely when waiting for the initial scrape
  to complete, it will wait 60s longer than the longest missing interval. (#171)

## [0.20.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.20.0) - 2021-03-22

### Changed
- Uses Prometheus `/api/v1/status/config` endpoint to read the Prometheus 
  config, to automatically determine the full set of scrape intervals. (#162)
- The startup timeout is raised to 10 minutes. (#166)

### Removed
- The `--prometheus.scrape-interval` option is ignored. (#162)

## [0.19.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.19.0) - 2021-03-15

### Added
- Adding `--healthcheck.threshold-ratio` to support tuning the acceptable error ratio
  when exporting metrics to a backend. (#146)
- Print metadata from gRPC response trailers. (#151)
- Added `sidecar.segment.skipped` counter to keep track of the number of times an
  event has caused the WAL to be skipped. (#155)
- Parsing and reporting on dropped metric points due to validation errors
  using Lightstep's conventions. (#157)

### Changed
- Fix metadata type conflict causing infinite loop due to change of instrument
  from histogram to another kind. (#151)
- Update Prometheus go.mod dependencies to match the 2.24.1 release. (#152)
- Update to [OTel-Go 0.18](https://github.com/open-telemetry/opentelemetry-go/releases/tag/v0.18.0). (#153)
- PrometheusReader handles truncated segment errors by raising an `ErrSkipSegment` which
  will trigger the tailer to skip to the next segment in process. (#155)
- Update to google.golang.org/protobuf v1.25.0, remove gogo dependency. (#156)

### Removed
- Field `corrupt-segment` has been removed from the progress file as the state is
  no longer needed now that the PrometheusReader handles this case. (#155)

## [0.18.3](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.18.3) - 2021-03-04

### Changed
- Fix issue that caused a segmentation failure on clean exit. (#143)
- Fix reset handling by checking against previous value instead of reset value. (#145)

### Added
- Added a check for minimum version of prometheus on start, report an error and exit if prometheus running
  is less version 2.10.0. (#144)

## [0.18.2](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.18.2) - 2021-02-26

### Changed
- Reduce the default value for max timeseries per request to 500. (#139)

## [0.18.1](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.18.1) - 2021-02-26

### Changed
- Reduce the default value for max shards to 200. (#139)

## [0.18.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.18.0) - 2021-02-26

### Added
- Sidecar waits for the first scrapes to complete before entering its
  run state. (#134)
- New setting `--prometheus.scrape-interval` supports configuring scrape
  interval(s) to wait for at startup. (#134)

### Changed
- The sidecar's WAL reader could get stuck in a restart loop in the event
  that the WAL's first segment after a checkpoint was truncated. The reader will
  now record the `corrupt-segment` in the progress log and skip the recorded
  segment on next restart (#136)

### Removed
- The `--startup.delay` setting has been removed in favor of monitoring when
  Prometheus actually finishes its first scrapes. (#134)

## [0.17.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.17.0) - 2021-02-23

### Added
- Automatically set (the same) `service.instance.id` for Destination/Diagnostics
  Resources. (#127)
- The sidecar's max timeseries per requests is now configurable
  via `prometheus.max-timeseries-per-request`. There is also a matching yaml configuration
  option: `max_timeseries_per_request`. (#128)
- The sidecar's max shards is now configurable via `--prometheus.max-shards`. There
  is also a matching yaml configuration option: `max_shards`. (#128)
- Adding metrcs to capture WAL size and the current offset. (#130)

### Changed
- The sidecar's WAL-reader addresses several race conditions by monitoring
  Prometheus for readiness and the current segment number during WAL segment
  transitions. (#118)
- The yaml section named "log_config" was inconsistent, has been renamed "log". ()

### Removed
- Remove the use_meta_labels parameter. (#125)

## [0.16.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.16.0) - 2021-02-18

### Added

- The sidecar's health check period is now configurable via `--healthcheck.period`. The metrics reporting
  period is automatically configured to half of the healthcheck period, since one depends on the other. (#117)
- Adds support for gzip compression and `none` compression, because `""` can't be configured via YAML. (#117)

### Changed
- Improved liveness checking. The sidecar starts in a healthy state and if it passes its selftest it then begins
  judging its own health after 5 healthcheck periods. After liveness fails, the supervisor will very rapidly report
  a crash report and shutdown its span reporter to flush its diagnostics. There is a chance that k8s may kill the
  process before the crash report is sent. (#117)
- Numerous small consistency and style improvements for logs in general. (#117)

## [0.15.1](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.15.1) - 2021-02-12

- Disabled target refresh and caching functionality (#115)
- WAL reader segment change-over race condition fixed. (#112)


## [0.14.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.14.0) - 2021-02-08

- Timeouts and diagnostics for Prometheus API calls (#100)
- Snappy compression support enabled by default (#97)
- Supervisor will kill the sidecar when there are no successful writes after
  repeated healthchecks. (#101)
- Print the number of dropped series in the supervisor health report. (#102)

## [0.13.1](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.13.1) - 2021-02-04

- Supervisor kills the process after repeated healthcheck failures. (#95)

## [0.13.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.13.0) - 2021-02-04

- Add `supervisor=true` in logs from the supervisor process. (#90)
- Add real `/-/health` health-checking implementation, with these criteria:
  sidecar.samples.produced should not stall over 5m
  sidecar.queue.outcome{outcome=success} divided by total {*} > 0.5 (#94)

## [0.12.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.12.0) - 2021-02-01

- Less resharding synchronization: do not require in-order writes (#87)
- Backstop against permanent Export() failures (#87)
- Rename all sidecar metrics to match `sidecar.*` (#87)
- Update to OTel-Go SDK v0.16.0. (#86)
- Use a 2 second maximum backoff when Export() fails (vs 100ms default). (#81)
- Update CI tests to use Prometheus 2.23. (#82)
- Update go.mod files to use Go 1.15. (#83)

## [0.11.1](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.11.1) - 2021-01-26

- Fix for graceful shutdown. (#79)

## [0.11.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.11.0) - 2021-01-25

- Enable self-diagnostics using the OTel-Go SDK, using the primary destination
  by default if none is configured.  Disable this behavior with --disable-diagnostics. (#72)
- Auto-downcase headers for http2 compliance. (#73)
- Ensures the sidecar will exit non-zero on errors. (#74)
- Adds a /-/ready endpoint, where readiness implies:
  - Prometheus is ready
  - Can write status file
  - Export empty request succeeds. (#75)
- Adds a /-/health endpoint, always returns OK. (#75)
- Adds a supervisor:
  - Performs periodic healthcheck
  - Runs main() in a sub-process, tees stdout and stderr
  - Records recent logs & healthcheck status in a span. (#75)
- Disable spans in the sidecar, replace w/ 2 Float64ValueRecorders. (#75)


## [0.10.0](https://github.com/lightstep/opentelemetry-prometheus-sidecar/releases/tag/v0.10.0) - 2021-01-21

- Fixes bug in WAL-tailer `openSegment()` method. (#71)

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
