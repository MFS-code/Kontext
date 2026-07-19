FROM golang:1.26.5 AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download

COPY pkg/result/ pkg/result/
COPY pkg/event/ pkg/event/
COPY runtimes/reporter/ runtimes/reporter/
COPY runtimes/reference/ runtimes/reference/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /kontext-reporter ./runtimes/reporter
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /kontext-reference ./runtimes/reference

FROM busybox:stable-musl AS shell
RUN mkdir /busybox-bin && \
    cp /bin/busybox /busybox-bin/busybox && \
    for applet in $(/bin/busybox --list); do \
      ln -s busybox "/busybox-bin/${applet}"; \
    done

FROM gcr.io/distroless/static:nonroot

COPY --from=shell /busybox-bin/ /bin/
COPY --from=builder /kontext-reporter /kontext-reporter
COPY --from=builder /kontext-reference /kontext-reference

USER 65532:65532
ENTRYPOINT ["/kontext-reporter", "--format", "kontext-envelope", "--", "/kontext-reference"]
