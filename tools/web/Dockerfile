FROM golang:1.27 AS build

WORKDIR /src

# Module graph first: this layer stays cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/web ./cmd/web

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/web /web

# MCP on /mcp (or, as `web fetcher`, POST /fetch) and health, 8080/tcp.
EXPOSE 8080/tcp

ENTRYPOINT ["/web"]
