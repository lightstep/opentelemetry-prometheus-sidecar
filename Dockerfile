FROM golang:1.15 as gotools

WORKDIR /gobuild

RUN go get github.com/fullstorydev/grpcurl/...
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go install github.com/fullstorydev/grpcurl/cmd/grpcurl

RUN git clone https://github.com/lightstep/otel-launcher-go.git

WORKDIR /gobuild/otel-launcher-go

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o otel-metrics-example ./examples/metrics
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o otel-tracing-example ./examples/tracing

WORKDIR /gobuild/prometheus-sidecar

COPY . .

WORKDIR /gobuild/prometheus-sidecar/telemetry

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o sidecar-telemetry-test ./cmd/sidecar-telemetry-test

FROM  praqma/network-multitool:latest

# Originally:
# FROM gcr.io/distroless/static:latest

LABEL maintainer "Lightstep Engineering <engineering@lightstep.com>"

COPY opentelemetry-prometheus-sidecar /bin/opentelemetry-prometheus-sidecar
COPY otlp-lightstep.protoset /protoset/otlp-lightstep
COPY otlp-proto.protoset /protoset/otlp-proto

COPY --from=gotools /go/bin/grpcurl /bin/grpcurl
COPY --from=gotools /gobuild/otel-launcher-go/otel-tracing-example /bin/otel-tracing-example
COPY --from=gotools /gobuild/otel-launcher-go/otel-metrics-example /bin/otel-metrics-example
COPY --from=gotools /gobuild/prometheus-sidecar/telemetry/sidecar-telemetry-test /bin/sidecar-telemetry-test

ENTRYPOINT [ "/bin/opentelemetry-prometheus-sidecar" ]
