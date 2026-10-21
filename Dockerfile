# popcorn for any Linux host. It only calls model APIs, so it needs no VMs or Docker inside.
#
#   docker build --build-arg VERSION=v1.2.3 -t popcorn .
#
# The image has no config: a deployment builds its own on top of it (FROM this image, COPY its
# config repo to /config), or mounts a checkout at /config. See docs/deploying.md.
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X github.com/metcalfc/popcorn/internal/version.Version=$VERSION" \
    -o /popcorn ./cmd/popcorn
# The image runs as nonroot, so /data has to exist and be theirs before a volume is mounted on it:
# a new named volume copies this directory's owner.
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /popcorn /app/popcorn
COPY --from=build --chown=nonroot:nonroot /data /data
# Your config repo: popcorn.toml with prompt-sets/ beside it.
ENV POPCORN_CONFIG=/config/popcorn.toml
ENV POPCORN_DATA_DIR=/data
VOLUME /data
EXPOSE 3000
# Listening beyond localhost requires POPCORN_TOKENS (popcorn refuses to start without it).
ENTRYPOINT ["/app/popcorn"]
CMD ["serve", "-addr", "0.0.0.0:3000"]
