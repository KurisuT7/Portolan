# syntax=docker/dockerfile:1
# Packages the binaries from scripts/build-release.sh. The release workflow
# publishes this image as ghcr.io/kurisut7/portolan for linux/amd64 and linux/arm64.

FROM --platform=$BUILDPLATFORM busybox:1.37 AS state
RUN mkdir /state && chmod 0700 /state

FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETARCH
COPY --chmod=0755 release/bin/${TARGETARCH}/portolan-panel release/bin/${TARGETARCH}/portolan-runtime-import /usr/local/lib/portolan-panel/
COPY --chmod=0755 scripts/install-agent.sh /usr/local/lib/portolan-panel/downloads/install-agent.sh
COPY --chmod=0755 release/bin/amd64/portolan-agent /usr/local/lib/portolan-panel/downloads/portolan-agent-linux-amd64
COPY --chmod=0755 release/bin/arm64/portolan-agent /usr/local/lib/portolan-panel/downloads/portolan-agent-linux-arm64
COPY LICENSE release/THIRD_PARTY_LICENSES /usr/share/doc/portolan/
# A named volume copies this directory's owner, so the panel can write to it.
COPY --from=state --chown=65532:65532 /state /var/lib/portolan-panel
ENV PORTOLAN_DATABASE=/var/lib/portolan-panel/portolan.db
VOLUME /var/lib/portolan-panel
USER 65532:65532
EXPOSE 8088
ENTRYPOINT ["/usr/local/lib/portolan-panel/portolan-panel"]
