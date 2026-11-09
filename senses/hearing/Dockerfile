FROM golang:1.27 AS build

WORKDIR /src

# Module graph first: this layer stays cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/hearing ./cmd/hearing

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/hearing /hearing

# MCP + health on 8080/tcp; one audio_in UDP port per session in 44000-44199.
EXPOSE 8080/tcp
EXPOSE 44000-44199/udp

ENTRYPOINT ["/hearing"]
