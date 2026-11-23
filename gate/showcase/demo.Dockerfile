# The demo's walker: curl for the person and the program, Python for gate-token.
FROM python:3.13-alpine
RUN apk add --no-cache curl
