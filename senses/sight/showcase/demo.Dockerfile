# The fake source: ffmpeg to stream a generated video over RTP, Python to
# drive sight's MCP, send words as SenML and check what came out.
FROM alpine:3.21
RUN apk add --no-cache ffmpeg python3
