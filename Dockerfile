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

# UID/GID for the in-container 'exedev' user. The Makefile passes the host
# user's UID/GID so bind-mounted files line up; both default to 1000.
ARG USER_UID=1000
ARG USER_GID=1000

# The prebuilt shelley .deb (from goreleaser), copied into the build context
# by the Makefile.
ARG SHELLEY_DEB=shelley.deb

ENV DEBIAN_FRONTEND=noninteractive

COPY ${SHELLEY_DEB} /tmp/shelley.deb

RUN set -eux; \
    apt-get update; \
    # openssh client + server, plus the shelley .deb (apt resolves its deps).
    apt-get install -y --no-install-recommends \
        ca-certificates \
        openssh-client \
        openssh-server; \
    apt-get install -y --no-install-recommends /tmp/shelley.deb; \
    rm -f /tmp/shelley.deb; \
    # Ensure an 'exedev' user/group owns the requested UID/GID. Ubuntu 24.04
    # ships a default 'ubuntu' user at 1000:1000, so when the ids already exist
    # we rename/adopt them; otherwise we create fresh entries.
    if getent group "${USER_GID}" >/dev/null; then \
        groupmod -n exedev "$(getent group "${USER_GID}" | cut -d: -f1)"; \
    else \
        groupadd -g "${USER_GID}" exedev; \
    fi; \
    if getent passwd "${USER_UID}" >/dev/null; then \
        existing="$(getent passwd "${USER_UID}" | cut -d: -f1)"; \
        usermod -l exedev -g "${USER_GID}" -d /home/exedev -m "${existing}"; \
    else \
        useradd -m -u "${USER_UID}" -g "${USER_GID}" -s /bin/bash exedev; \
    fi; \
    # sshd's privilege-separation directory.
    mkdir -p /var/run/sshd; \
    # Drop apt caches so the flattened final layer stays small.
    apt-get clean; \
    rm -rf /var/lib/apt/lists/*

# Final stage: flatten everything from the build stage into a single layer.
# FROM scratch carries no metadata, so anything the runtime needs (PATH, user,
# workdir) is re-declared here.
FROM scratch
COPY --from=build / /

ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
ENV HOME=/home/exedev
USER exedev
WORKDIR /home/exedev
EXPOSE 9000
CMD ["shelley", "serve"]
