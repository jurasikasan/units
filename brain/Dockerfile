FROM golang:1.27.3 AS build

WORKDIR /src

# Module graph first: this layer stays cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/brain ./cmd/brain

FROM alpine:3.22

RUN adduser -D -H -u 65532 brain

COPY --from=build /out/brain /brain

USER 65532

# GET /health on 8080; nothing else comes in: the senses arrive by Redis.
EXPOSE 8080/tcp

ENTRYPOINT ["/brain"]
