# OpenTelemetry Prometheus sidecar

> ❗ **This sidecar is no longer recommend**. Please the OpenTelemetry Collector Prometheus receiver instead, [documentation on setting up and configuring the collector for Prometheus](https://docs.lightstep.com/docs/replace-prometheus-with-an-otel-collector-on-kubernetes) is available on the [Lightstep Observability Learning Portal](https://docs.lightstep.com).

This repository contains a sidecar for the
[Prometheus](https://prometheus.io/) Server that sends metrics data to
an [OpenTelemetry](https://opentelemetry.io) Metrics Protocol endpoint.  This
software is derived from the [Stackdriver Prometheus
Sidecar](https://github.com/Stackdriver/stackdriver-prometheus-sidecar).

![OpenTelemetry Prometheus Sidecar Diagram](docs/img/opentelemetry-prometheus-sidecar.png)

## Repository Status (2/21/2021)

This repository is being maintained by Lightstep and will be donated
to OpenTelemetry.  [We are moving this
repository](https://github.com/open-telemetry/community/issues/575)
into the [OpenTelemetry](https://opentelemetry.io/)
[organization](http://github.com/open-telemetry) and [will continue
development on a public
fork](https://github.com/open-telemetry/prometheus-sidecar) of the
[upstream Stackdriver Prometheus
sidecar](https://github.com/Stackdriver/stackdriver-prometheus-sidecar)
repository.

This code base is 100% OpenTelemetry and Prometheus, not a
Lightstep-specific sidecar, functioning to read data collected and
written by Prometheus, convert into the OpenTelemetry data model, and
write to an OpenTelemetry endpoint.

This sidecar sends [OpenTelemetry Protocol](https://github.com/open-telemetry/opentelemetry-specification/blob/master/specification/protocol/otlp.md) [version 0.7](https://github.com/open-telemetry/opentelemetry-proto/releases/tag/v0.7.0) (or later versions) over gRPC.

## Sidecar design

The sidecar includes:

1. Prometheus write-ahead log (WAL) reader
2. Metadata cache that tracks active instruments
3. Configured settings:
* Additional resource attributes to apply to all metric timeseries
* Renaming and prefixing to change the name of metric timeseries
* Filters to avoid reporting specific metric timeseries
* Specify whether to use use int64 (optional) vs. double (default) protocol encoding

Sidecar operates by continually:
1. Reading the prometheus WAL log (package retrieval and tail);
1. Refreshing its view of the instrument metadata (package metadata);
1. Transforming WAL samples into OpenTelemetry Protocol(OTLP) metrics (package retrieval);
1. Sending OTLP metrics to the destination endpoint (package otlp).


Resources to understand how the WAL log works can be found [here](https://prometheus.io/docs/prometheus/latest/storage/) and [here](https://ganeshvernekar.com/blog/prometheus-tsdb-wal-and-checkpoint/).

## Installation

Lightstep publishes Docker images of this binary named
`lightstep/opentelemetry-prometheus-sidecar:${VERSION}`, with the
latest release always tagged `latest`.

To build from source, please clone this repository.  You will build a
Docker image, push it to a private container registry, and then run
the container as described below.  To test and build a Docker image
for the current operating system, simply:

```
export DOCKER_IMAGE_NAME=my.image.reposito.ry/opentelemetry/prometheus-sidecar
export DOCKER_IMAGE_TAG=$(cat ./VERSION)
make docker
docker push ${DOCKER_IMAGE_NAME}:${DOCKER_IMAGE_TAG}
```

## Deployment

The sidecar is deployed next to an already running Prometheus server.

An example command-line:

```
opentelemetry-prometheus-sidecar \
  --destination.endpoint=${DESTINATION} \
  --destination.header="lightstep-access-token=${VALUE}" \
  --destination.attribute="service.name=${SERVICE}" \
  --diagnostics.endpoint=${DIAGNOSTICS} \
  --diagnostics.header="lightstep-access-token=${VALUE}" \
  --diagnostics.attribute="service.name=${SERVICE}" \
  --prometheus.wal=${WAL} \
  --prometheus.endpoint=${PROMETHEUS} \
```

where:

* `DESTINATION`: Destination address https://host:port for sending prometheus metrics
* `DIAGNOSTICS`: Diagnostics address https://host:port for sending sidecar telemetry
* `VALUE`: Value for the `Custom-Header` request header
* `SERVICE`: Value for the `service.name` resource attribute
* `WAL`: Prometheus' WAL directory, defaults to `data/wal`
* `PROMETHEUS`: URL of the Prometheus UI.

[See the list of command-line flags below](#configuration).

Settings can also be passed through a configuration file.  For example:

```
destination:
  endpoint: https://otlp.io:443
  headers:
    Custom-Header: custom-value
  attributes:
    service.name: my-service-name
diagnostics:
  endpoint: https://otlp.io:443
  headers:
    Custom-Header: custom-value
  attributes:
    service.name: my-service-name
prometheus:
  wal: /prometheus/wal
  endpoint: http://192.168.10.10:9191
```

[See an example configuration yaml file here](./config/sidecar.example.yaml)

The sidecar requires write access to the directory to store its progress between restarts.

### Kubernetes and Helm setup

To configure the sidecar for a Prometheus server installed using the
[Prometheus Community Helm Charts](https://github.com/prometheus-community/helm-charts).

#### Sidecar with Prometheus Helm chart
To configure the core components of the Prometheus sidecar, add the following definition to your custom `values.yaml`:

```
server:
  sidecarContainers:
  - name: otel-sidecar
    image: lightstep/opentelemetry-prometheus-sidecar
    imagePullPolicy: Always
    args:
    - --prometheus.wal=/data/wal
    - --destination.endpoint=$(DESTINATION)
    - --destination.header=lightstep-access-token=AAAAAAAAAAAAAAAA
    - --diagnostics.endpoint=$(DIAGNOSTIC)
    - --diagnostics.header=lightstep-access-token=AAAAAAAAAAAAAAAA
    volumeMounts:
    - name: storage-volume
      mountPath: /data
    ports:
    - name: admin-port
      containerPort: 9091
    livenessProbe:
      httpGet:
        path: /-/health
        port: admin-port
      periodSeconds: 30
      failureThreshold: 2
```
#### Sidecar with kube-prometheus-stack Helm chart
To configure the sidecar using the Prometheus Operator via the [kube-prometheus-stack](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack) helm chart:

*NOTE: the volume configured in the sidecar must be the same volume as prometheus uses*

```
prometheus:
  prometheusSpec:
    containers:
      - name: otel-sidecar
        image: lightstep/opentelemetry-prometheus-sidecar:latest
        imagePullPolicy: Always

        args:
        - --prometheus.wal=/prometheus/prometheus-db/wal
        - --destination.endpoint=$(DESTINATION)
        - --destination.header=lightstep-access-token=AAAAAAAAAAAAAAAA
        - --diagnostics.endpoint=$(DIAGNOSTIC)
        - --diagnostics.header=lightstep-access-token=AAAAAAAAAAAAAAAA

        #####
        ports:
        - name: admin-port
          containerPort: 9091

        #####
        livenessProbe:
          httpGet:
            path: /-/health
            port: admin-port
          periodSeconds: 30
          failureThreshold: 2

        #####
        resources:
          requests:
            ephemeral-storage: "50M"

        volumeMounts:
        - name: prometheus-db
          mountPath: /prometheus

```

The [upstream Stackdriver Prometheus sidecar Kubernetes
README](https://github.com/Stackdriver/stackdriver-prometheus-sidecar/blob/master/kube/README.md)
contains more examples of how to patch an existing Prometheus
deployment or deploy the sidecar without using Helm.

### Configuration

Most sidecar configuration settings can be set through command-line
flags, while a few more rarely-used options are only settable through
a yaml configuration file.  To see all available command-line flags,
run `opentelemetry-prometheus-sidecar --help`.  The printed usage is
shown below:

```
usage: opentelemetry-prometheus-sidecar [<flags>]

The OpenTelemetry Prometheus sidecar runs alongside the Prometheus
(https://prometheus.io/) Server and sends metrics data to an OpenTelemetry
(https://opentelemetry.io) Protocol endpoint.

Flags:
  -h, --help                     Show context-sensitive help (also try
                                 --help-long and --help-man).
      --version                  Show application version.
      --config-file=CONFIG-FILE  A configuration file.
      --destination.endpoint=DESTINATION.ENDPOINT
                                 Destination address of a OpenTelemetry Metrics
                                 protocol gRPC endpoint (e.g.,
                                 https://host:port). Use "http" (not "https")
                                 for an insecure connection.
      --destination.attribute=DESTINATION.ATTRIBUTE ...
                                 Destination resource attributes attached to
                                 OTLP data (e.g., MyResource=Value1). May be
                                 repeated.
      --destination.header=DESTINATION.HEADER ...
                                 Destination headers used for OTLP requests
                                 (e.g., MyHeader=Value1). May be repeated.
      --destination.timeout=DESTINATION.TIMEOUT
                                 Destination timeout used for OTLP Export()
                                 requests
      --destination.compression=DESTINATION.COMPRESSION
                                 Destination compression used for OTLP requests
                                 (e.g., snappy, gzip, none).
      --diagnostics.endpoint=DIAGNOSTICS.ENDPOINT
                                 Diagnostics address of a OpenTelemetry Metrics
                                 protocol gRPC endpoint (e.g.,
                                 https://host:port). Use "http" (not "https")
                                 for an insecure connection.
      --diagnostics.attribute=DIAGNOSTICS.ATTRIBUTE ...
                                 Diagnostics resource attributes attached to
                                 OTLP data (e.g., MyResource=Value1). May be
                                 repeated.
      --diagnostics.header=DIAGNOSTICS.HEADER ...
                                 Diagnostics headers used for OTLP requests
                                 (e.g., MyHeader=Value1). May be repeated.
      --diagnostics.timeout=DIAGNOSTICS.TIMEOUT
                                 Diagnostics timeout used for OTLP Export()
                                 requests
      --diagnostics.compression=DIAGNOSTICS.COMPRESSION
                                 Diagnostics compression used for OTLP requests
                                 (e.g., snappy, gzip, none).
      --prometheus.wal=PROMETHEUS.WAL
                                 Directory from where to read the Prometheus
                                 TSDB WAL. Default: data/wal
      --prometheus.endpoint=PROMETHEUS.ENDPOINT
                                 Endpoint where Prometheus hosts its UI, API,
                                 and serves its own metrics. Default:
                                 http://127.0.0.1:9090/
      --prometheus.max-point-age=PROMETHEUS.MAX-POINT-AGE
                                 Skip points older than this, to assist
                                 recovery. Default: 25h0m0s
      --prometheus.health-check-request-timeout=PROMETHEUS.HEALTH-CHECK-REQUEST-TIMEOUT  
                                 Timeout used for health-check requests to the prometheus endpoint. Default: 5s
      --prometheus.scrape-interval=PROMETHEUS.SCRAPE-INTERVAL ...
                                 Ignored. This is inferred from the Prometheus
                                 via api/v1/status/config
      --admin.port=ADMIN.PORT    Administrative port this process listens on.
                                 Default: 9091
      --admin.listen-ip=ADMIN.LISTEN-IP
                                 Administrative IP address this process listens
                                 on. Default: 0.0.0.0
      --security.root-certificate=SECURITY.ROOT-CERTIFICATE ...
                                 Root CA certificate to use for TLS connections,
                                 in PEM format (e.g., root.crt). May be
                                 repeated.
      --opentelemetry.max-bytes-per-request=OPENTELEMETRY.MAX-BYTES-PER-REQUEST
                                 Send at most this many bytes per request.
                                 Default: 65536
      --opentelemetry.min-shards=OPENTELEMETRY.MIN-SHARDS
                                 Min number of shards, i.e. amount of
                                 concurrency. Default: 1
      --opentelemetry.max-shards=OPENTELEMETRY.MAX-SHARDS
                                 Max number of shards, i.e. amount of
                                 concurrency. Default: 200
      --opentelemetry.metrics-prefix=OPENTELEMETRY.METRICS-PREFIX
                                 Customized prefix for exporter metrics. If not
                                 set, none will be used
      --filter=FILTER ...        PromQL metric and attribute matcher which must
                                 pass for a series to be forwarded to
                                 OpenTelemetry. If repeated, the series must
                                 pass any of the filter sets to be forwarded.
      --startup.timeout=STARTUP.TIMEOUT
                                 Timeout at startup to allow the endpoint to
                                 become available. Default: 10m0s
      --healthcheck.period=HEALTHCHECK.PERIOD
                                 Period for internal health checking; set at a
                                 minimum to the shortest Promethues scrape
                                 period
      --healthcheck.threshold-ratio=HEALTHCHECK.THRESHOLD-RATIO
                                 Threshold ratio for internal health checking.
                                 Default: 0.5
      --log.level=LOG.LEVEL      Only log messages with the given severity or
                                 above. One of: [debug, info, warn, error]
      --log.format=LOG.FORMAT    Output format of log messages. One of: [logfmt,
                                 json]
      --log.verbose=LOG.VERBOSE  Verbose logging level: 0 = off, 1 = some, 2 =
                                 more; 1 is automatically added when log.level
                                 is 'debug'; impacts logging from the gRPC
                                 library in particular
      --leader-election.enabled  Enable leader election to choose a single writer.
      --leader-election.k8s-namespace=LEADER-ELECTION.K8S-NAMESPACE  
                                 Namespace used for the leadership election lease.
      --disable-supervisor       Disable the supervisor.
      --disable-diagnostics      Disable diagnostics by default; if unset,
                                 diagnostics will be auto-configured to the
                                 primary destination

```

Two kinds of sidecar customization are available only through the
configuration file.  An [example sidecar yaml configuration documents
the available options](./config/sidecar.example.yaml).

Command-line and configuration files can be used at the same time,
where command-line parameter values override configuration-file
parameter values, with one exception.  Configurations that support
a map from string to string, including both request headers and
resource attributes, are combined from both sources.

#### Startup safety

The sidecar waits until Prometheus finishes its first scrape(s) to
begin processing the WAL, to ensure that target information is
available before the sidecar tries loading its metadata cache.

This information is obtained through the Prometheus
`api/v1/status/config` endpoint.  The sidecar will log which intervals
it is waiting for during startup.  When using very long scrape
intervals, raise the `--startup.timeout` setting so the sidecar will
wait long enough to begin running.

#### Throughput and memory configuration

The following settings give the user control over the amount of memory
used by the sidecar for concurrent RPCs.

The sidecar uses a queue to manage distributing data points from the
Prometheus WAL to a variable number of workers, referred to as
"shards".  Each shard assembles a limited size request, determined by
`--opentelemetry.max-bytes-per-request` (default: 64kB).

The number of shards varies in response to load, based on the observed
latency.  Upper and lower bounds on the number of shards are
configured by `--opentelemetry.min-shards` and
`--opentelemetry.max-shards`.

CPU usage is determined by the number of shards and the workload.  Use
`--opentelemetry.max-shards` to limit the maximum CPU used by the
sidecar.

#### Validation errors

The sidecar reports validation errors using conventions established by
Lightstep for conveying information about _partial success_ when
writing to the OTLP destination.  These errors are returned using gRPC
"trailers" (a.k.a. http2 response headers) and are output as metrics
and logs.  See the `sidecar.metrics.failing` metric to diagnose
validation errors.

#### Metadata errors

The sidecar may encounter errors between itself and Prometheus,
including failures to locate metadata about a targets that Prometheus
no longer knows about.  Missing metadata, Prometheus API errors, and
other forms of inconsistency are reported using
`sidecar.metrics.failing` with `key_reason` and `metric_name` attributes.

#### OpenTelemetry Resource Attributes

Use the `--destination.attribute=KEY=VALUE` flag to add additional OpenTelemetry resource attributes to all exported timeseries.

#### Prometheus External Labels

Prometheus external labels are used for diagnostic purposes but are not attached to exported timeseries.

#### Filters

The `--filter` flag allows to provide filters which all series have to pass before being sent to the destination. Filters use the same syntax as [Prometheus instant vector selectors](https://prometheus.io/docs/prometheus/latest/querying/basics/#instant-vector-selectors), e.g.:

```
opentelemetry-prometheus-sidecar --filter='{__name__!~"cadvisor_.+",job="k8s"}' ...
```

This drops all series which do not have a `job` label `k8s` and all metrics that have a name starting with `cadvisor_`.

For equality filter on metric name you can use the simpler notation, e.g. `--filter='metric_name{label="foo"}'`.

The flag may be repeated to provide several sets of filters, in which case the metric will be forwarded if it matches at least one of them.

#### Metric renames

To change the name of a metric as it is exported, use the
`metric_renames` section in the configuration file:

```yaml
metric_renames:
  - from: original_metric_name
    to: new_metric_name
# - ...
```

#### Static metadata

To change the output type, value type, or description of a metric
instrument as it is exported, use the `static_metadata` section in the
configuration file:

```yaml
static_metadata:
  - metric: some_metric_name
    type: counter # or gauge, or histogram
    value_type: double # or int64
    help: an arbitrary help string
# - ...
```

Note:

* All `static_metadata` entries must have `type` specified.
* If `value_type` is specified, it will override the default value type for counters and gauges. All Prometheus metrics have a default type of double.

#### High Availability Prometheus Setup

In a HA prometheus setup, a prometheus sidecar can be attached to each replica. All sidecars will write all metric points, it is responsibility of the backend of choice to deduplicate these points.

The leader election can be enabled in order to restrict which replica will send metrics to the Collector, reducing the amount of metrics transferred on the wire.

One of the sidecars will be elected as the Leader, this leader sidecar will tail the Prometheus WAL log, transform and send OTLP metrics to the Collector. All other non-leader sidecars will be in a stand-by mode, it will tail the Prometheus WAL log, but will not send any data to the Collector.

If the leader sidecar fails, a new Leader will be elected and will resume sending data to the collector.

The leader election uses the kubernetes coordination API to elect a leader. Ensure that there is a service account for Prometheus in Kubernetes and then bind it to the role with the following permissions:
```yaml
rules:
  - apiGroups:
      - coordination.k8s.io
    resources:
      - leases
    verbs:
      - '*'
```

After to the service account permissions are set up, set the argument flag  `--leader-election.enabled` on the prometheus sidecar.

To change the namespace used for the leadership election lease, set `--leader-election.k8s-namespace=LEADER-ELECTION.K8S-NAMESPACE`.

## Monitoring

When run in the default configuration, the sidecar will self-monitor for liveness and kill the process when it becomes unhealthy.  Sidecar liveness can be monitored directly using the `/-/health` endpoint.  We recommend a period of 30 seconds and `failureThreshold: 2`, for example in your Kubernetes deployment:

```
    ports:
    - name: admin-port
      containerPort: 9091
    livenessProbe:
      httpGet:
        path: /-/health
        port: admin-port
      periodSeconds: 30
      failureThreshold: 2
```

## Diagnostics

The sidecar is instrumented with the OpenTelemetry-Go SDK and runs with standard instrumentation packages, including runtime and host metrics.

By default, diagnostics are autoconfigured to the primary destination.  Configuration options are available to disable or configure alternative destinations and options.

### Configuration Options

Separate diagnostics settings can be configure to output OTLP similar to configuring the primary destination, for example:

```
diagnostics:
  endpoint: https://otel-collector:443
  headers:
    Custom-Header: custom-value
  timeout: timeout-value
  attributes:
    extra.resource: extra-value
```

Likewise, these fields can be accessed using `--diagnostics.endpoint`,
`--diagnostics.header`, `--diagnostics.timeout`, and `--diagnostics.attribute`.

#### Log levels

The Prometheus sidecar provides options for logging in the case of diagnosing an issue.
* We recommend starting with setting the `--log.level` to be `debug`, `info`, `warn`, `error`.
* Additional options are available to set the output format of the logs (`--log.format` to be `logfmt` or `json`), and the number of logs to recorded (`--log.verbose` to be `0` for off, `1` for some, `2` for more)

#### Disabling Diagnostics

To disable diagnostics, set the argument flag `--disable-diagnostics`.

### Diagnostic Outputs

The Prometheus Sidecar has two main processes, the supervisor and a subordinate process.  Diagnostic traces are sent from the supervisor process to monitor lifecycle events and diagnostic metrics are sent from the subordinate process.

#### Sidecar Supervisor Tracing

Traces from the supervisor process are most helpful for diagnosing unknown problems.  It signals lifecycle events and captures the stderr, attaching it to the trace.

The sidecar will output spans from its supervisor process with service.name=`opentelemetry-prometheus-sidecar-supervisor`.  There are two kinds of spans: operation=`health-client` and operation=`shutdown-report`.  These spans are also tagged with the attribute `sidecar-health` with values `ok`, `not yet ready` or `first time ready`.

#### Sidecar Metrics

Metrics from the subordinate process can help identify issues once the first metrics are successfully written.  There are three standard host and runtime metrics to monitor:

**Key Success Metrics**

These metrics are key to understanding the health of the sidecar.
They are periodically printed to the console log to assist in
troubleshooting.

| Metric Name | Type | Description | Additional Attributes |
| --- | --- | --- | ---|
| sidecar.points.produced | counter | number of points read from the prometheus WAL | |
| sidecar.points.dropped | counter | number of points dropped due to errors | `key_reason`: metadata, validation |
| sidecar.points.skipped | counter | number of points skipped by filters | |
| sidecar.queue.outcome | counter | outcome of the sample in the queue | `outcome`: success, failed, retry, aborted |
| sidecar.series.dropped | counter | number of series or metrics dropped | `key_reason`: metadata, validation |
| sidecar.series.current | gauge | number of series in the cache | `status`: live, filtered, invalid |
| sidecar.metrics.failing | gauge | failing metric names and explanations | `key_reason`, `metric_name` |

**Progress Metrics**

Two metrics indicate the sidecar's progress in processing the
Prometheus write-ahead-log (WAL).  These are measured in bytes and are
based on the assumption that each WAL segment is a complete 128MB.
Both of these figures are exported as `current_segment_number *
128MB + segment_offset`.  The difference (i.e., `sidecar.wal.size -
sidecar.wal.offset`) indicates how far the sidecar has to catch up,
assuming complete WAL segments.

| Metric Name | Type | Description |
| --- | --- | --- |
| sidecar.wal.size | gauge | current writer position of the Prometheus write-ahead log |
| sidecar.wal.offset | gauge | current reader position in the Prometheus write-ahead log |

**Host and Runtime Metrics**

| Metric Name | Type | Description | Additional Attributes |
| --- | --- | --- | ---|
| process.cpu.time | counter | cpu seconds used | `state`: user, sys |
| system.network.io | counter | bytes sent and received | `direction`: read, write |
| runtime.go.mem.heap_alloc | gauge | memory in use | |

**Internal Metrics**

These metrics are diagnostic in nature, meant for characterizing
performance of the code and individual Prometheus installations.
Operators can safely ignore these metrics except to better understand
sidecar performance.

| Metric Name | Type | Description | Additional Attributes |
| --- | --- | --- | ---|
| sidecar.connect.duration | histogram | how many attempts to connect (and how long) | `error`: true, false |
| sidecar.export.duration | histogram | how many attempts to export (and how long) | `error`: true, false |
| sidecar.monitor.duration | histogram | how many attempts to scrape Prometheus /metrics (and how long) | `error`: true, false |
| sidecar.metadata.fetch.duration | histogram | how many attempts to fetch metadata from Prometheus (and how long) | `mode`: single, batch; `error`: true, false |
| sidecar.queue.capacity | gauge | number of available slots for samples (i.e., points) in the queue, counts buffer size times current number of shards | |
| sidecar.queue.running | gauge | number of running shards, those which have not exited | |
| sidecar.queue.shards | gauge | number of current shards, as set by the queue manager | |
| sidecar.queue.size | gauge | number of samples (i.e., points) standing in a queue waiting to export | |
| sidecar.series.defined | counter | number of series defined in the WAL | |
| sidecar.metadata.lookups | counter | number of calls to lookup metadata | `error`: true, false |
| sidecar.refs.collected | counter | number of WAL series refs removed from memory by garbage collection | `error`: true, false |
| sidecar.refs.notfound | counter | number of WAL series refs that were not found during lookup | |
| sidecar.segment.opens | counter | number of WAL segment open() calls | |
| sidecar.segment.reads | counter | number of WAL segment read() calls | |
| sidecar.segment.bytes | counter | number of WAL segment bytes read | |
| sidecar.segment.skipped | counter | number of skipped WAL segments | |
| sidecar.leadership | gauge | indicate if this sidecar is the leader or not | |

## Upstream

This repository was copied into a private reposotitory from [this upstream fork](https://github.com/Stackdriver/stackdriver-prometheus-sidecar/tree/1361301230bcfc978864a8f4c718aba98bc07a3d) of `stackdriver-prometheus-sidecar`, dated July 31, 2020.

## Compatibility

The matrix below lists the versions of Prometheus Server and other dependencies that have been qualified to work with releases of `opentelemetry-prometheus-sidecar`. If the matrix does not list whether they are compatible, please assume they are not verified yet but can be compatible. Feel free to contribute to the matrix if you have run the end-to-end test between a version of `opentelemetry-prometheus-sidecar` and Prometheus server.

| Sidecar Version | Compatible Prometheus Server Version(s)   | Incompatible Prometheus Server Version(s) |
| -- | -- | -- |
| **0.1.x** | 2.15, 2.16, 2.18, 2.19, 2.21, 2.22, 2.23, 2.24 | 2.5 |

## Troubleshooting

The following describes known scenarios that can be problematic for the sidecar.

### Sidecar is falling behind

It's possible for the sidecar to not have enough resources by default to keep up with the WAL in high throughput scenarios. When this case occurs, the following message will be displayed in the sidecar's logs:

```
past WAL segment not found, sidecar may have dragged behind. Consider increasing min-shards, max-shards and max-bytes-per-request value
```

This message means that the sidecar is looking for a WAL segment file that has been removed, usually due to Prometheus triggering a checkpoint. It's possible to look at the delta between `sidecar.wal.size` (total wal entries) and `sidecar.wal.offset` (where the sidecar currently is) to determine if the sidecar has enough resources to keep up. If the offset is increasingly further behind the size, it's recommended to increase the timeseries emitted per request using the following configuration options:

- `--opentelemetry.max-bytes-per-request` configures the maximum number of timeseries sent with each request from the sidecar to the OTLP backend.
- `--opentelemetry.max-shards` configures the number of parallel go routines and grpc connections used to transmit the data.
