FROM golang:1.26.11 AS build
WORKDIR /workspace
COPY sdk/go/ ./sdk/go/
COPY examples/support/ ./examples/support/
WORKDIR /workspace/examples/support
RUN CGO_ENABLED=0 go build -trimpath -o /support . && go version /support

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /support /usr/local/bin/support
USER 65532:65532
CMD ["/usr/local/bin/support"]
