FROM golang:1.27.3 AS build

WORKDIR /src

# Module graph first: this layer stays cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/jurasikasan/units/body.Version=${VERSION}" -o /out/body ./cmd/body

FROM alpine:3.22

RUN adduser -D -H -u 65532 body

COPY --from=build /out/body /body

USER 65532

# MCP on /mcp, GET /body and GET /health on 8080. No sound or light comes in
# here: the body's ears and eyes are its senses' sessions.
EXPOSE 8080/tcp

ENTRYPOINT ["/body"]
