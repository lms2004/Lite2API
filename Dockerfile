FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/lite2api ./cmd/lite2api

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && addgroup -g 10001 lite2api && adduser -D -H -u 10001 -G lite2api lite2api && install -d -o 10001 -g 10001 -m 700 /app/data
WORKDIR /app
COPY --from=build /out/lite2api /usr/local/bin/lite2api
COPY --chown=10001:10001 --chmod=600 config.example.json /app/data/config.json
USER 10001:10001
EXPOSE 45679
ENTRYPOINT ["lite2api"]
CMD ["-config", "/app/data/config.json"]
