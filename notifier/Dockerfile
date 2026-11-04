FROM golang:1.27.3 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .

ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -ldflags "-X main.version=${VERSION:-dev}" -o /out/notifier ./cmd/app

FROM alpine:3.22

RUN apk add --no-cache ca-certificates

COPY --from=builder /out/notifier /usr/local/bin/notifier

CMD ["notifier"]
