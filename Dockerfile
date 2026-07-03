# Consumed by GoReleaser: it copies the already cross-compiled binary out of
# the build context rather than compiling, so the image build is fast and
# uses the same static binary every other artifact ships.
#
# ari is pure Go with no runtime dependency beyond CA roots, so the image is
# a minimal Alpine with ca-certificates, tzdata, and git (ari works inside
# repos). Mount the project at /work and the nest persists under /home/ari.
#
# GoReleaser builds one multi-platform image with buildx and stages each
# platform's binary under a $TARGETPLATFORM directory (e.g. linux/amd64/) in
# the build context, so the COPY line selects the right one through the
# automatic TARGETPLATFORM build arg.
FROM alpine:3.21

ARG TARGETPLATFORM

RUN apk add --no-cache ca-certificates tzdata git \
 && adduser -D -u 10001 ari \
 && mkdir -p /work \
 && chown ari:ari /work

COPY $TARGETPLATFORM/ari /usr/bin/ari

USER ari
WORKDIR /work

# A headless run against a mounted repo:
#
#   docker run -v "$PWD:/work" -e ANTHROPIC_API_KEY ghcr.io/tamnd/ari -p "fix the failing test" --json
#
ENV HOME=/home/ari

VOLUME ["/work"]

ENTRYPOINT ["/usr/bin/ari"]
