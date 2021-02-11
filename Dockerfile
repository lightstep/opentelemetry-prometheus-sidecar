FROM  gcr.io/distroless/static:latest
LABEL maintainer "Lightstep Engineering <engineering@lightstep.com>"

COPY opentelemetry-prometheus-sidecar /bin/opentelemetry-prometheus-sidecar

ENTRYPOINT [ "/bin/opentelemetry-prometheus-sidecar" ]
