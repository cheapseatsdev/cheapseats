# cheapseats for any Linux host. It only calls model APIs, so it needs no VMs or Docker inside.
#
#   docker build --build-arg VERSION=v1.2.3 -t cheapseats .
#
# The image has no config: a deployment builds its own on top of it (FROM this image, COPY its
# config repo to /config), or mounts a checkout at /config. See docs/deploying.md.
# Base images are pinned by digest (the tag is for reading); Dependabot's docker entry moves both.
FROM --platform=$BUILDPLATFORM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X github.com/cheapseatsdev/cheapseats/internal/version.Version=$VERSION" \
    -o /cheapseats ./cmd/cheapseats
# The image runs as nonroot, so /data has to exist and be theirs before a volume is mounted on it:
# a new named volume copies this directory's owner.
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
WORKDIR /app
COPY --from=build /cheapseats /app/cheapseats
COPY --from=build --chown=nonroot:nonroot /data /data
# Your config repo: cheapseats.toml with prompt-sets/ beside it.
ENV CHEAPSEATS_CONFIG=/config/cheapseats.toml
ENV CHEAPSEATS_DATA_DIR=/data
VOLUME /data
EXPOSE 3000
# Listening beyond localhost requires CHEAPSEATS_TOKENS (cheapseats refuses to start without it).
ENTRYPOINT ["/app/cheapseats"]
CMD ["serve", "-addr", "0.0.0.0:3000"]
