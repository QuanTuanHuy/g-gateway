FROM alpine:3.21.3 AS builder

ARG WRK_COMMIT=a211dd5a7050b1f9e8a9870b95513060e72ac4a0

RUN apk add --no-cache build-base git linux-headers openssl-dev perl
COPY monotonic-clock.patch /tmp/monotonic-clock.patch
RUN git clone https://github.com/wg/wrk.git /wrk \
    && git -C /wrk checkout --detach ${WRK_COMMIT} \
    && test "$(git -C /wrk rev-parse HEAD)" = "${WRK_COMMIT}" \
    && git -C /wrk apply --check /tmp/monotonic-clock.patch \
    && git -C /wrk apply /tmp/monotonic-clock.patch \
    && git -C /wrk diff --check \
    && make -C /wrk -j"$(getconf _NPROCESSORS_ONLN)"

FROM alpine:3.21.3

ARG WRK_COMMIT=a211dd5a7050b1f9e8a9870b95513060e72ac4a0

RUN apk add --no-cache libgcc openssl \
    && addgroup -g 10001 benchmark \
    && adduser -D -u 10001 -G benchmark benchmark
COPY --from=builder /wrk/wrk /usr/local/bin/wrk

LABEL org.opencontainers.image.source="https://github.com/wg/wrk" \
      org.opencontainers.image.revision="${WRK_COMMIT}" \
      io.g-gateway.benchmark.generator="wrk-4.2.0" \
      io.g-gateway.benchmark.patch="monotonic-clock"

USER 10001:10001
WORKDIR /results
ENTRYPOINT ["/usr/local/bin/wrk"]
