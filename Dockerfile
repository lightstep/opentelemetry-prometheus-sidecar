FROM  gcr.io/distroless/static:latest
LABEL maintainer "Lightstep Engineering <engineering@lightstep.com>"

COPY lightstep-prometheus-sidecar         /bin/lightstep-prometheus-sidecar

EXPOSE     9091
ENTRYPOINT [ "/bin/lightstep-prometheus-sidecar" ]
