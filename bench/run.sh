#!/usr/bin/env bash

set -e

pushd "$( dirname "${BASH_SOURCE[0]}" )"

go build github.com/lightstep/opentelemetry-prometheus-sidecar/cmd/opentelemetry-prometheus-sidecar

trap 'kill 0' SIGTERM

echo "Starting Prometheus"

prometheus \
  --storage.tsdb.min-block-duration=15m \
  --storage.tsdb.retention=48h 2>&1 | sed -e "s/^/[prometheus] /" &

echo "Starting server"

go run main.go --latency=30ms 2>&1 | sed -e "s/^/[server] /" &

sleep 2
echo "Starting sidecar"

./opentelemetry-prometheus-sidecar \
  --config-file="sidecar.yml" \
  --web.listen-address="0.0.0.0:9091" \
  --opentelemetry.generic.location="test-cluster" \
  --opentelemetry.generic.namespace="test-namespace" \
  --opentelemetry.endpoint="http://127.0.0.1:9092/?auth=false" \
  2>&1 | sed -e "s/^/[sidecar] /" &

if [ -n "${SIDECAR_OLD}" ]; then
  echo "Starting old sidecar"
  
  ${SIDECAR_OLD} \
    --log.level=debug \
    --web.listen-address="0.0.0.0:9093" \
    --opentelemetry.debug \
    --opentelemetry.endpoint="http://127.0.0.1:9092/?auth=false" \
    2>&1 | sed -e "s/^/[sidecar-old] /" &
fi

wait

popd
