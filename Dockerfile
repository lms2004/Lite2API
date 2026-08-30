FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN GOTOOLCHAIN=local CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/lite2api ./cmd/lite2api

FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
RUN apk add --no-cache ca-certificates tzdata && addgroup -g 10001 lite2api && adduser -D -H -u 10001 -G lite2api lite2api && install -d -o 10001 -g 10001 -m 700 /app/data
WORKDIR /app
COPY --from=build /out/lite2api /usr/local/bin/lite2api
COPY --chown=10001:10001 config.example.json /app/data/config.json
# Keep the Dockerfile compatible with the classic builder used on older hosts;
# COPY --chmod requires BuildKit even though a normal chmod does not.
RUN chmod 0600 /app/data/config.json
USER 10001:10001
EXPOSE 45679
ENTRYPOINT ["lite2api"]
CMD ["-config", "/app/data/config.json"]
