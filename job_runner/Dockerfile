FROM golang:1.27.3 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags "-X main.version=${VERSION:-dev}" -o job_runner ./cmd/app

FROM alpine:3.19
WORKDIR /app
COPY --from=builder /app/job_runner .
EXPOSE 8080
CMD ["./job_runner"]
