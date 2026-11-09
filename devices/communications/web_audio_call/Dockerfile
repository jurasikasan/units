FROM golang:1.27-alpine AS build
RUN apk add --no-cache build-base opus-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -tags nolibopusfile -ldflags "-s -w" -o /out/webrtc_line .

FROM alpine:3.20
RUN apk add --no-cache opus
COPY --from=build /out/webrtc_line /usr/local/bin/webrtc_line
EXPOSE 8080 45000-45099/udp 45100-45199/udp
ENTRYPOINT ["webrtc_line"]
