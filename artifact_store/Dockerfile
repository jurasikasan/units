# Build from this directory:
#   docker build -t artifact_store .

FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags '-s -w' -o /out/artifact-store ./cmd/app

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/artifact-store /usr/local/bin/artifact-store
ENV PORT=8094
EXPOSE 8094
ENTRYPOINT ["artifact-store"]
