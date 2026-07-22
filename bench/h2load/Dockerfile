FROM debian:bookworm-slim AS builder

ARG NGHTTP2_TAG=v1.69.0

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        autoconf \
        automake \
        build-essential \
        ca-certificates \
        git \
        libc-ares-dev \
        libev-dev \
        libjansson-dev \
        libssl-dev \
        libtool \
        libxml2-dev \
        pkg-config \
        zlib1g-dev \
    && rm -rf /var/lib/apt/lists/*
RUN git clone --depth 1 --branch ${NGHTTP2_TAG} --recurse-submodules --shallow-submodules https://github.com/nghttp2/nghttp2.git /nghttp2 \
    && cd /nghttp2 \
    && autoreconf -i \
    && ./configure --disable-python-bindings --disable-examples \
    && make -j"$(getconf _NPROCESSORS_ONLN)" \
    && make install DESTDIR=/install

FROM debian:bookworm-slim

ARG NGHTTP2_TAG=v1.69.0

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        libc-ares2 \
        libev4 \
        libjansson4 \
        libssl3 \
        libxml2 \
        zlib1g \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 benchmark \
    && useradd --uid 10001 --gid benchmark --no-create-home benchmark
COPY --from=builder /install/usr/local/bin/h2load /usr/local/bin/h2load
COPY --from=builder /install/usr/local/lib/libnghttp2.so* /usr/local/lib/
RUN ldconfig

LABEL org.opencontainers.image.source="https://github.com/nghttp2/nghttp2" \
      org.opencontainers.image.version="${NGHTTP2_TAG}" \
      io.g-gateway.benchmark.generator="h2load-${NGHTTP2_TAG}"

USER 10001:10001
WORKDIR /results
ENTRYPOINT ["/usr/local/bin/h2load"]
