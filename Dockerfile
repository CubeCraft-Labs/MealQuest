# syntax=docker/dockerfile:1
# Base images are pinned by digest (multi-arch index); tags are for readability.

FROM node:22-alpine@sha256:0a7108bf6c7bf5de370ffb1a3ed6be93d405b43ff159f681a8d18c0e2bc2e402 AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59 AS server
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Replace the placeholder with the built PWA; recreate the directory so no
# stale file survives into the embed.
RUN rm -rf internal/frontend/dist
COPY --from=web /src/web/dist/ internal/frontend/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/mealquest ./cmd/server

# Alpine ships the CA bundle and busybox wget for the health check.
FROM alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8
RUN addgroup -S -g 10001 mealquest \
    && adduser -S -D -H -u 10001 -G mealquest mealquest \
    && mkdir /data && chown mealquest:mealquest /data
COPY --from=server /out/mealquest /usr/local/bin/mealquest
ENV MEALQUEST_HTTP_ADDR=:8080 \
    MEALQUEST_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
USER 10001:10001
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/health || exit 1
ENTRYPOINT ["/usr/local/bin/mealquest"]
