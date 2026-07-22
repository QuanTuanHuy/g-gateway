# Derived from docker/debian-dev/Dockerfile at the APISIX commit guarded by
# bench/run.ps1. The embedded Dashboard artifact is intentionally omitted:
# this benchmark disables both the Admin API and UI and measures data-plane
# behavior only.

FROM debian:bullseye-slim AS build

ARG ENABLE_PROXY=false
ARG CODE_PATH

ENV DEBIAN_FRONTEND=noninteractive
ENV ENV_INST_LUADIR=/usr/local/apisix

COPY ${CODE_PATH} /apisix

WORKDIR /apisix

# Docker Desktop sends Windows working-tree line endings in the context.
# Re-materialize the guarded commit inside the image after Git is installed.
RUN set -x \
    && apt-get -y update --fix-missing \
    && apt-get install -y \
        make \
        git \
        sudo \
        ca-certificates \
        curl \
        gcc \
        g++ \
        cmake \
        libyaml-dev \
        libxml2-dev \
        libxslt1-dev \
        pkg-config \
        libssl-dev \
        zlib1g-dev \
    && git config --global --add safe.directory /apisix \
    && git -C /apisix config core.autocrlf false \
    && git -C /apisix reset --hard HEAD \
    && ls -al \
    && make deps \
    && mkdir -p ${ENV_INST_LUADIR} \
    && cp -r deps ${ENV_INST_LUADIR} \
    && make install

FROM debian:bullseye-slim

ARG ENTRYPOINT_PATH=./docker-entrypoint.sh
ARG INSTALL_BROTLI=./install-brotli.sh

RUN apt-get -y update --fix-missing \
    && apt-get install -y libldap2-dev libyaml-0-2 libxml2 libxslt1.1 \
    && apt-get remove --purge --auto-remove -y \
    && mkdir -p /usr/local/apisix/ui

COPY --from=build /usr/local/apisix /usr/local/apisix
COPY --from=build /usr/local/openresty /usr/local/openresty
COPY --from=build /usr/bin/apisix /usr/bin/apisix

COPY ${INSTALL_BROTLI} /install-brotli.sh
RUN sed -i 's/\r$//' /install-brotli.sh \
    && chmod +x /install-brotli.sh \
    && cd / \
    && ./install-brotli.sh \
    && rm -rf /install-brotli.sh \
    && chgrp -R 0 /usr/local/apisix \
    && chmod -R g=u /usr/local/apisix

ENV PATH=$PATH:/usr/local/openresty/luajit/bin:/usr/local/openresty/nginx/sbin:/usr/local/openresty/bin

WORKDIR /usr/local/apisix

RUN ln -sf /dev/stdout /usr/local/apisix/logs/access.log \
    && ln -sf /dev/stderr /usr/local/apisix/logs/error.log

EXPOSE 9080 9443

COPY ${ENTRYPOINT_PATH} /docker-entrypoint.sh
RUN sed -i 's/\r$//' /docker-entrypoint.sh \
    && chmod +x /docker-entrypoint.sh

ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["docker-start"]
STOPSIGNAL SIGQUIT
