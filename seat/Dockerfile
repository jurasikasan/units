FROM golang:1.27.3 AS build

WORKDIR /src

# Module graph first: this layer stays cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/jurasikasan/units/seat.Version=${VERSION}" -o /out/seat ./cmd/seat

FROM alpine:3.22

RUN adduser -D -H -u 65532 seat

COPY --from=build /out/seat /seat

USER 65532

# The door (POST /v1/chat/completions), the seats' HTTP, MCP on /mcp and
# GET /health, all on 8080.
EXPOSE 8080/tcp

ENTRYPOINT ["/seat"]
