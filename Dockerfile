FROM golang:1.25-alpine AS go-build
WORKDIR /src
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
# internal/webui/dist is generated and verified by `npm run build` before LPK
# packaging.  Keeping the validated assets in the build context avoids a
# second, network-dependent npm installation inside Docker.
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/aim-console.sparse ./cmd/aim-console && \
    dd if=/out/aim-console.sparse of=/out/aim-console bs=1M && \
    chmod 0755 /out/aim-console && rm /out/aim-console.sparse

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S -g 10001 aim && adduser -S -D -H -u 10001 -G aim aim && \
    install -d -o aim -g aim -m 0750 /var/lib/aim-console /usr/share/aim
COPY --from=go-build /out/ /usr/share/aim/
USER aim
EXPOSE 8080
# Keep the image metadata compatible with the lzcos/docker image loader used
# by older landan boxes.  The LazyCat manifest bind below remains the actual
# persistent storage path and overrides this image-declared volume.
VOLUME ["/var/lib/aim-console"]
ENTRYPOINT ["/usr/share/aim/aim-console"]
