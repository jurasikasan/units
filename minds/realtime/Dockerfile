FROM golang:1.27.3 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
# The tree this binary is built from, shown by /health.
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags "-X main.version=${VERSION:-dev}" -o /out/realtime ./cmd/app

FROM alpine:3.22
WORKDIR /app
COPY --from=builder /out/realtime .
EXPOSE 8080
HEALTHCHECK --interval=2s --retries=30 CMD wget -qO- http://127.0.0.1:${PORT:-8080}/health >/dev/null || exit 1
CMD ["./realtime"]
