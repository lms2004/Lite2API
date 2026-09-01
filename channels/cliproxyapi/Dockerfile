FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS builder

RUN apk add --no-cache ca-certificates git
WORKDIR /src

# Fetch the audited upstream revision instead of copying a potentially dirty
# developer submodule into the production image.
RUN git init \
    && git remote add origin https://github.com/router-for-me/CLIProxyAPI.git \
    && git fetch --depth=1 origin 785b00c3127eea6aa207f1207ead8a2aa93690a3 \
    && git checkout --detach FETCH_HEAD \
    && test "$(git rev-parse HEAD)" = "785b00c3127eea6aa207f1207ead8a2aa93690a3"

COPY deploy/patches/cliproxyapi-quota-snapshot.patch /patches/cliproxyapi-quota-snapshot.patch
COPY deploy/patches/cliproxyapi-routing-reliability.patch /patches/cliproxyapi-routing-reliability.patch
COPY deploy/patches/cliproxyapi-auth-refresh.patch /patches/cliproxyapi-auth-refresh.patch
COPY deploy/patches/cliproxyapi-credential-routing.patch /patches/cliproxyapi-credential-routing.patch
RUN printf '%s  %s\n' \
      bbb9e08f18b210f9ddfbd958ee3cfb84cacc1b8d6b9c02b9c14ac8e23e490a68 /patches/cliproxyapi-quota-snapshot.patch \
      0a388faa429991cccc348bcddc31b0d350ddc21a36681da1bfbca5d052b39f37 /patches/cliproxyapi-routing-reliability.patch \
      f5e482c127994eb31c9419bbce1e82fff39bfb893ef8c38b14e875b780a330a5 /patches/cliproxyapi-auth-refresh.patch \
      d0e4923a56dabd12f7cbae421ca9c624bbd4e372fc22b47d95e7644c140bc131 /patches/cliproxyapi-credential-routing.patch \
      | sha256sum -c - \
    && git apply --unidiff-zero --check /patches/cliproxyapi-quota-snapshot.patch \
    && git apply --unidiff-zero /patches/cliproxyapi-quota-snapshot.patch \
    && git apply --unidiff-zero --check /patches/cliproxyapi-routing-reliability.patch \
    && git apply --unidiff-zero /patches/cliproxyapi-routing-reliability.patch \
    && git apply --unidiff-zero --check /patches/cliproxyapi-auth-refresh.patch \
    && git apply --unidiff-zero /patches/cliproxyapi-auth-refresh.patch \
    && git apply --unidiff-zero --check /patches/cliproxyapi-credential-routing.patch \
    && git apply --unidiff-zero /patches/cliproxyapi-credential-routing.patch

ARG VERSION=v6.10.9-lite2api.8
ARG COMMIT=785b00c3127eea6aa207f1207ead8a2aa93690a3
ARG BUILD_DATE=2026-09-01
RUN test "$COMMIT" = "785b00c3127eea6aa207f1207ead8a2aa93690a3" \
    && GOTOOLCHAIN=local CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
      -o /out/CLIProxyAPI ./cmd/server/

FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 cliproxyapi \
    && adduser -D -H -u 10001 -G cliproxyapi cliproxyapi \
    && install -d -o 10001 -g 10001 -m 0700 /CLIProxyAPI /run/cliproxyapi/auths
WORKDIR /CLIProxyAPI
COPY --from=builder /out/CLIProxyAPI /CLIProxyAPI/CLIProxyAPI
COPY --from=builder /src/config.example.yaml /CLIProxyAPI/config.example.yaml
COPY channels/cliproxyapi/healthcheck.sh /usr/local/bin/cliproxy-healthcheck
# Portable to the classic Docker builder (COPY --chmod is BuildKit-only).
RUN chmod 0555 /usr/local/bin/cliproxy-healthcheck
ENV TZ=UTC
USER 10001:10001
EXPOSE 45682
ENTRYPOINT ["/CLIProxyAPI/CLIProxyAPI"]
CMD ["-config", "/CLIProxyAPI/config.yaml"]
