# Lightstep Prometheus sidecar

This repository contains a sidecar for the
[Prometheus](https://prometheus.io/) Server that sends metrics data to
an [OpenTelemetry](https://opentelemetry.io) Protocol endpoint.  This
software is derived from the [Stackdriver Prometheus
Sidecar](https://github.com/Stackdriver/stackdriver-prometheus-sidecar).

![OpenTelemetry Prometheus Sidecar Diagram](docs/img/opentelemetry-prometheus-sidecar.png)

## OpenTelemetry Design

In OpenTelemetry, the basic process or instrumented unit of
computation is described by a _resource_.  The primary function of
this sidecar is to read data pulled by Prometheus, covert it to the
OpenTelemetry data model, and finally write it to an OpenTelemetry
endpoint.

This sidecar sends [OpenTelemetry Protocol](https://github.com/open-telemetry/opentelemetry-specification/blob/master/specification/protocol/otlp.md) [version 0.5](https://github.com/open-telemetry/opentelemetry-proto/releases/tag/v0.5.0) over gRPC.

## Prometheus Design

The Prometheus server consists of a number of interacting parts that
relate to the sidecar.

1. Service discovery. Prometheus has over 15 builtin service discovery strategies, which serve to fetch and dynamically update the set of targets.
2. Job configuration. A Prometheus job is identified by the name of its confuration, which includes service dicovery, followed by [_target relabeling_](https://prometheus.io/docs/prometheus/latest/configuration/configuration/#relabel_config), followed by [_general relabeling_](https://prometheus.io/docs/prometheus/latest/configuration/configuration/#metric_relabel_configs).
3. Label discovery and relabeling.  Service discovery-specific labels, prefixed by `__meta_`, are made available for use during relabeling rules.  Prometheus relabeling configurations must choose which meta-labels to output through relabeling, otherwise no `__`-prefixed keys are kept.  All timeseries have `job` and `instance` labels.
4. In-memory metadata database.  The Prometheus server maintains metadata about both active targets and active metric instruments in memory, including the "discovered" meta-labels.
5. Write-ahead log.  After targets discovered, their meta-labels are synthesized, and relabeling steps are taken, the output following each collection is written to a write-ahead log.  Other Prometheus components consume this log.

Critically, the Prometheus write-ahead log does not include timeseries metadata that the Prometheus server expects will be collected again soon, including whether the timeseries represents a counter or a gauge.  Prometheus can be configured to write its write-ahead-log to a remote destination, but systems built on this mechanism must refer back to Prometheus servers or otherwise obtain metadata about the kind of data that is in the log.  Note that Prometheus [_is taking efforts to add metadata to its write-ahead-log_](https://github.com/prometheus/prometheus/pull/6815), though it [appears unlikely to make it into the 2.23 release](https://github.com/prometheus/prometheus/pull/7771#issuecomment-707639237).

## Sidecar design

The sidecar includes:

1. Prometheus write-ahead log reader
2. Target cache that tracks active targets by their identifying labels
3. Metadata cache that tracks active instruments, by target
4. Configured settings:
* Extra resource labels to apply to all metric timeseries
* Renaming and prefixing to change the name of metric timeseries
* Filters to avoid reporting specific metric timeseries
* Specify whether to use use int64 (optional) vs. double (default) protocol encoding
* Whether to include all meta-labels as resource labels.

The sidecar operates by continually (and concurrently) reading the
log, refreshing its view of targets and instrument metadata,
transforming the data into OpenTelemetry Protocol metrics, and sending
over gRPC to an OpenTelemetry metrics service.

### Target label discovery

The sidecar uses Prometheus server HTTP `api/v1/targets/metadata` API
to obtain metadata about active collection targets and metric
instruments.  The result of target metadata retrieval includes:

1. The set of identifying target labels, which include the application
metric labels plus those applied during Prometheus relabeling rules; this always includes `job` and `instance`
2. The set of "discovered" target labels, which includes Prometheus metadata (e.g., `__scheme__`, `__address__`) and the service-discovery meta-labels (e.g., `__meta_kubernetes_pod_name`).

When reporting timeseries to output destination, the identifying
target labels are included as OpenTelemetry resource attributes.  The
`--resource.attribute` flag can be used to add addional constant
labels as resource attributes.  The `--resource.use-meta-labels` flag
can be used to add all meta labels as resource attribuets.  Otherwise,
labels beginning with `__` are dropped.

## Installation

Please clone this repository and build the sidecar from source.  You
will build a Docker image, push it to a private container registry,
and then run the container as described below.  To test and build a
Docker image for the current operating system, simply:

```
git clone https://github.com/lightstep/opentelemetry-prometheus-sidecar.git
cd opentelemetry-prometheus-sidecar
make
docker build .
```

To package a linux-amd64 binary:

```
git clone https://github.com/lightstep/opentelemetry-prometheus-sidecar.git
cd opentelemetry-prometheus-sidecar
make build-linux-amd64
docker build .
```

## Deployment

The sidecar is deployed next to an already running Prometheus server.
An example command-line:

```
opentelemetry-prometheus-sidecar \
  --prometheus.wal-directory=${WAL} \
  --opentelemetry.endpoint=${DESTINATION} \
  --grpc.header="Lightstep-Access-Token=${TOKEN}" \
  --prometheus.api-address=${API_ADDRESS} \
```

where:

* `WAL`: Prometheus' WAL directory, defaults to `data/wal`
* `DESTINATION`: Destination address host:port, set this to `ingest.lightstep.com:443`
* `TOKEN`: A Lightstep access token (example header value)
* `API_ADDRESS`: Prometheus' API address, defaults to `127.0.0.1:9090`

The sidecar requires write access to the directory to store its progress between restarts.

If your Prometheus server itself is running inside of Kubernetes, the example [Kubernetes setup](./kube/README.md)
can be used as a reference for setup.

### Configuration

The majority of configuration options for the sidecar are set through flags. To see all available flags, run `opentelemetry-prometheus-sidecar --help`.  The printed usage is shown below:

```
TODO
```

TODO: Document and test the configuration file format mechanism.

#### Resources

Use the `--destination.attribute=KEY=VALUE` flag to add additional resource attributes to all exported timeseries.

Use the `--destination.use-meta-labels` flag to add discovery meta-labels to all exported timeseries.

#### Filters

The `--include` flag allows to provide filters which all series have to pass before being sent to the destination. Filters use the same syntax as [Prometheus instant vector selectors](https://prometheus.io/docs/prometheus/latest/querying/basics/#instant-vector-selectors), e.g.:

```
opentelemetry-prometheus-sidecar --include='{__name__!~"cadvisor_.+",job="k8s"}' ...
```

This drops all series which do not have a `job` label `k8s` and all metrics that have a name starting with `cadvisor_`.

For equality filter on metric name you can use the simpler notation, e.g. `--include='metric_name{label="foo"}'`.

The flag may be repeated to provide several sets of filters, in which case the metric will be forwarded if it matches at least one of them.

#### File

The sidecar can also be provided with a configuration file. It allows to define static metric renames and to overwrite metric metadata which is usually provided by Prometheus. A configuration file should not be required for the majority of users.

```yaml
metric_renames:
  - from: original_metric_name
    to: new_metric_name
# - ...

static_metadata:
  - metric: some_metric_name
    type: counter # or gauge/histogram
    value_type: double # or int64
    help: an arbitrary help string
# - ...
```

Static metadata allows overriding metadata used for output timeseries.  Note:

* All `static_metadata` entries must have `type` specified.
* If `value_type` is specified, it will override the default value type for counters and gauges. All Prometheus metrics have a default type of double.

## Upstream

This repository was copied into a private reposotitory from [this upstream fork](https://github.com/Stackdriver/stackdriver-prometheus-sidecar/tree/1361301230bcfc978864a8f4c718aba98bc07a3d) of `stackdriver-prometheus-sidecar`, dated July 31, 2020.

Changes relative to `stackdriver-prometheus-sidecar` include:

* Replace Stackdriver monitoring protocol with OTLP v0.5; this was straightforward since these are similar protocols
* Add `--grpc.header` support for adding gRPC metadata
* Remove "Resource Map" code, used for generating "Monitored Resource" concept in Stackdriver; OpenTelemetry is less restrictive, this code is replaced by `--resource.attribute` and `--resource.use-meta-labels` support
* Remove GCP/GKE-specific automatic resources; these can be applied using `--resource.attribute`
* Remove "Counter Aggregator" support, which pre-aggregates labels; there are other ways this could be implemented, if the OpenTelemetry-Go SDK were used to generate OTLP instead of the dedicated code in this repository
* Add `--security.root-certificate` support for supplying the root certificate used in TLS connection setup.

## Compatibility

The matrix below lists the versions of Prometheus Server and other dependencies that have been qualified to work with releases of `opentelemetry-prometheus-sidecar`. If the matrix does not list whether they are compatible, please assume they are not verified yet but can be compatible. Feel free to contribute to the matrix if you have run the end-to-end test between a version of `opentelemetry-prometheus-sidecar` and Prometheus server.

| Sidecar Version | Compatible Prometheus Server Version(s)   | Incompatible Prometheus Server Version(s) |
| -- | -- | -- |
| **0.1.x**       | 2.10, 2.11, 2.13, 2.15, 2.16, 2.18, 2.19, 2.21 | 2.5                                       |
