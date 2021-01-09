#!/bin/sh

NAME=lightstep/opentelemetry-prometheus-sidecar
LABEL=${1}

make docker DOCKER_IMAGE_NAME=${NAME} DOCKER_IMAGE_TAG=${LABEL}
docker push ${NAME}:${LABEL}
