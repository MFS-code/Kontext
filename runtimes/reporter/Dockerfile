FROM golang:1.26.5 AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download

COPY pkg/result/ pkg/result/
COPY runtimes/reporter/ runtimes/reporter/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /kontext-reporter ./runtimes/reporter

FROM gcr.io/distroless/static:nonroot

COPY --from=builder /kontext-reporter /kontext-reporter

USER 65532:65532
ENTRYPOINT ["/kontext-reporter"]
