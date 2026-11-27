FROM golang:1.27 AS build

WORKDIR /src

# Module graph first: this layer stays cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/sight ./cmd/sight

# Not distroless: sight decodes H.264 through an ffmpeg process (CONTRACT §4.1),
# so the image carries ffmpeg; Alpine's is the smallest that does.
FROM alpine:3.21

RUN apk add --no-cache ffmpeg && adduser -D -H -u 65532 sight

COPY --from=build /out/sight /sight

USER 65532

# MCP, sight_in (POST /in/{session_id}) and health on 8080/tcp; one video_in
# port pair per session in 45000-45199/udp (RTP on the even port, RTCP above).
EXPOSE 8080/tcp
EXPOSE 45000-45199/udp

ENTRYPOINT ["/sight"]
