FROM golang:1.15 as gotools

WORKDIR /gobuild

RUN go get github.com/fullstorydev/grpcurl/...
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go install github.com/fullstorydev/grpcurl/cmd/grpcurl

RUN git clone https://github.com/lightstep/otel-launcher-go.git

WORKDIR /gobuild/otel-launcher-go

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o otel_metrics_example ./examples/metrics
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o otel_tracing_example ./examples/tracing

FROM  praqma/network-multitool:latest

# Originally:
# FROM gcr.io/distroless/static:latest

LABEL maintainer "Lightstep Engineering <engineering@lightstep.com>"

COPY opentelemetry-prometheus-sidecar /bin/opentelemetry-prometheus-sidecar
COPY otlp-lightstep.protoset /protoset/otlp-lightstep
COPY otlp-proto.protoset /protoset/otlp-proto

COPY --from=gotools /go/bin/grpcurl /bin/grpcurl
COPY --from=gotools /gobuild/otel-launcher-go/otel_tracing_example /bin/otel_tracing_example
COPY --from=gotools /gobuild/otel-launcher-go/otel_metrics_example /bin/otel_metrics_example

ENTRYPOINT [ "/bin/opentelemetry-prometheus-sidecar" ]
