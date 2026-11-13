FROM golang:1.27.3 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .

# The tree this binary is built from, shown by /health; the build context has
# no .git, so the value comes from whoever builds it.
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -ldflags "-X main.version=${VERSION:-dev}" -o /out/main ./cmd/app

FROM alpine:3.22
WORKDIR /root/
COPY --from=builder /out/main .
EXPOSE 8080
CMD ["./main"]
