FROM golang:1.27.3 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -o /out/main ./cmd/app && \
    CGO_ENABLED=0 GOOS=linux go build -o /out/hashpw ./cmd/hashpw && \
    CGO_ENABLED=0 GOOS=linux go build -o /out/disable ./cmd/disable

FROM alpine:3.22
WORKDIR /root/
COPY --from=builder /out/main /out/hashpw /out/disable ./

CMD ["./main"]
