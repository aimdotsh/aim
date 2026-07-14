FROM node:22-alpine AS web-build
WORKDIR /src
COPY web/package.json web/package-lock.json ./web/
RUN cd web && npm ci --no-audit --no-fund
COPY web ./web
RUN cd web && npm run build

FROM golang:1.25-alpine AS go-build
WORKDIR /src
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web-build /src/internal/webui/dist ./internal/webui/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/aim-console ./cmd/aim-console && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/aim-executor ./cmd/aim-executor

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S -g 10001 aim && adduser -S -D -H -u 10001 -G aim aim && \
    install -d -o aim -g aim -m 0750 /var/lib/aim-console /usr/share/aim
COPY --from=go-build /out/aim-console /usr/local/bin/aim-console
COPY --from=go-build /out/aim-executor /usr/share/aim/aim-executor
COPY aim.sh /usr/share/aim/aim.sh
USER aim
EXPOSE 8080
VOLUME ["/var/lib/aim-console"]
ENTRYPOINT ["/usr/local/bin/aim-console"]
