FROM  gcr.io/distroless/static:latest
LABEL maintainer "Lightstep Engineering <engineering@lightstep.com>"

COPY opentelemetry-prometheus-sidecar /bin/opentelemetry-prometheus-sidecar

EXPOSE 9091

ENTRYPOINT [ "/bin/opentelemetry-prometheus-sidecar" ]
