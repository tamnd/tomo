# Consumed by GoReleaser: it copies the already cross-compiled binary out of the
# build context rather than compiling, so the image build is fast and uses the
# same static binary every other artifact ships.
#
# tomo is a pure-Go, CGO-free binary with no runtime dependencies beyond CA
# certificates, so the image is tiny: just the static binary on a minimal base.
#
# GoReleaser builds one multi-platform image with buildx and stages each
# platform's binary under a $TARGETPLATFORM directory (e.g. linux/amd64/) in the
# build context, so the COPY line selects the right one through the automatic
# TARGETPLATFORM build arg.
FROM alpine:3.21

ARG TARGETPLATFORM

# ca-certificates for HTTPS to model providers; tzdata for sane timestamps.
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -H -u 10001 tomo \
 && mkdir -p /data \
 && chown tomo:tomo /data

COPY $TARGETPLATFORM/tomo /usr/bin/tomo

USER tomo
WORKDIR /data

# tomo keeps its config, ledger, and memory under $HOME/.tomo. Pointing HOME at
# the mounted volume keeps all of that on the host, so a container restart does
# not forget anything:
#
#   docker run --rm -it -v "$PWD/tomo-data:/data" \
#     -e ANTHROPIC_API_KEY ghcr.io/tamnd/tomo chat
#
# For the daemon, publish the web chat port and run serve:
#
#   docker run --rm -p 8765:8765 -v "$PWD/tomo-data:/data" \
#     -e ANTHROPIC_API_KEY ghcr.io/tamnd/tomo serve --addr 0.0.0.0:8765
ENV HOME=/data

EXPOSE 8765

VOLUME ["/data"]

ENTRYPOINT ["/usr/bin/tomo"]
