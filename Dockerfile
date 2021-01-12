FROM golang:1.15 as gotools

LABEL maintainer "Lightstep Engineering <engineering@lightstep.com>"

WORKDIR /gobuild/prometheus-sidecar

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o opentelemetry-prometheus-sidecar ./cmd/opentelemetry-prometheus-sidecar

FROM gcr.io/distroless/static:latest

COPY --from=gotools /gobuild/prometheus-sidecar/opentelemetry-prometheus-sidecar /bin/opentelemetry-prometheus-sidecar

ENTRYPOINT [ "/bin/opentelemetry-prometheus-sidecar" ]
