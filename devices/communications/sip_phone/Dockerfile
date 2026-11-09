FROM golang:1.27.3 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags "-X main.version=${VERSION:-dev}" -o phone ./cmd/app

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /src/phone .
EXPOSE 8080 5070/udp
CMD ["./phone"]
