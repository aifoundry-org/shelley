# syntax=docker/dockerfile:1
#
# Runs Shelley inside a container based on Ubuntu 24.04.
#
# The image is built in two stages: the "build" stage installs everything on
# top of Ubuntu 24.04, and the final stage copies the whole filesystem into a
# single layer (FROM scratch) so we don't ship the bytes of the intermediate
# apt/dpkg layers. See the `docker` target in the Makefile for how this is
# driven (goreleaser builds the .deb, which is then installed here).

FROM ubuntu:24.04 AS build

# The prebuilt shelley .deb (from goreleaser), copied into the build context
# by the Makefile.
ARG SHELLEY_DEB=shelley.deb

ENV DEBIAN_FRONTEND=noninteractive

COPY ${SHELLEY_DEB} /tmp/shelley.deb

RUN set -eux; \
    apt-get update; \
    # openssh client + server and sudo. The shelley .deb depends on
    # ca-certificates, so apt pulls that in when the package is installed below.
    apt-get install -y --no-install-recommends \
        openssh-client \
        openssh-server \
        sshfs \
        sudo; \
    # Installing the .deb runs its postinstall, which creates the 'shelley'
    # system user (home /var/lib/shelley) that the service runs as.
    apt-get install -y --no-install-recommends /tmp/shelley.deb; \
    # smithd filesystem loans authenticate this exact non-root runtime user
    # with a one-time public key and run a forced SSHFS command. OpenSSH rejects
    # locked accounts and accounts whose shell is nologin, even when password
    # authentication is disabled.
    usermod --shell /bin/sh shelley; \
    passwd --delete shelley; \
    rm -f /tmp/shelley.deb; \
    # The system user has no password, so grant passwordless sudo explicitly.
    printf '%s\n' 'shelley ALL=(ALL:ALL) NOPASSWD:ALL' > /etc/sudoers.d/shelley; \
    chmod 0440 /etc/sudoers.d/shelley; \
    visudo -c; \
    # sshd's privilege-separation directory.
    mkdir -p /var/run/sshd; \
    # Drop apt caches so the flattened final layer stays small.
    apt-get clean; \
    rm -rf /var/lib/apt/lists/*

# Final stage: flatten everything from the build stage into a single layer.
# FROM scratch carries no metadata, so anything the runtime needs (PATH, HOME,
# user, workdir) is re-declared here.
FROM scratch
COPY --from=build / /

ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
ENV HOME=/var/lib/shelley
# Run as the 'shelley' system user provisioned by the .deb's postinstall; it
# owns /var/lib/shelley, where the SQLite DB lives.
USER shelley
WORKDIR /var/lib/shelley
EXPOSE 9000
CMD ["/usr/bin/shelley", "-config", "/etc/shelley.json", "-db", "/var/lib/shelley/shelley.db", "serve"]
