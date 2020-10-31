#!/bin/sh

set -e
set -u

if [  $# -lt 1 ]; then
  echo -e "Usage: $0 <prometheus_name>\n"
  exit 1
fi

kubectl -n "${KUBE_NAMESPACE}" patch prometheus "$1" --type merge --patch "
spec:
  containers:
  - name: sidecar
    image: ${SIDECAR_IMAGE_NAME}:${SIDECAR_IMAGE_TAG}
    imagePullPolicy: Always
    args:
    - \"--prometheus.wal-directory=/data/wal\"
    - \"--opentelemetry.endpoint=https:${OTLP_DESTINATION}\"
    - \"--grpc.header=Lightstep-Access-Token=${TOKEN}\"
    - \"--resource.attribute=region=${GCP_REGION}\"
    - \"--resource.attribute=cluster=${KUBE_CLUSTER}\"
    ports:
    - name: sidecar
      containerPort: 9091
    volumeMounts:
    - mountPath: /data
      name: prometheus-$1-db
"
