# job_runner's daemon: Docker in Docker with gVisor registered as runsc, and
# the egress rule (dind-egress.sh) as its entrypoint. The box that runs it
# needs Docker and nothing else. x86_64 only: gVisor's release for it.
FROM alpine:3.22 AS gvisor
ARG GVISOR=20261102.0
ARG GVISOR_SHA512=2e6671bb2d033b871c5811ab57e16ac570988b0e0054c525f911cfaf191bab3ca4efb08e0a98daa4b9e7d248f9e5776ef2ef2f8a6b82d829646a42924c98bb05
RUN apk add --no-cache curl bzip2 \
 && curl -fsSLo /gvisor.tar.bz2 "https://storage.googleapis.com/gvisor/releases/release/${GVISOR}/x86_64/gvisor.tar.bz2" \
 && echo "${GVISOR_SHA512}  /gvisor.tar.bz2" | sha512sum -c - \
 && mkdir /out && tar -C /out -xjf /gvisor.tar.bz2 runsc gvisor-bin

FROM docker:29.6.0-dind
# runsc finds its sidecars in gvisor-bin beside it.
COPY --from=gvisor /out/runsc /usr/local/bin/runsc
COPY --from=gvisor /out/gvisor-bin /usr/local/bin/gvisor-bin
COPY dind-egress.sh /usr/local/bin/dind-egress.sh
# dockerd listens here only, in a volume job_runner shares; the docker CLI in
# this container (the egress script, a healthcheck) talks to it here too.
ENV DOCKER_HOST=unix:///var/run/dind/docker.sock DOCKER_TLS_CERTDIR=
ENTRYPOINT ["/usr/local/bin/dind-egress.sh"]
