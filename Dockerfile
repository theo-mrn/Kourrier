# syntax=docker/dockerfile:1.7

# Web console (ADR 016): built once, embedded in the binary of every platform.
FROM --platform=$BUILDPLATFORM node:24-alpine AS console
RUN npm install -g pnpm@11.15.0
WORKDIR /src/web/console
COPY web/console/package.json web/console/pnpm-lock.yaml web/console/pnpm-workspace.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile
COPY web/console/ ./
# Writes /src/internal/console/dist.
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=console /src/internal/console/dist ./internal/console/dist
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/kourrier ./cmd/kourrier

# Distroless static: no shell, no package manager, runs as UID 65532.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/kourrier /usr/local/bin/kourrier
USER nonroot:nonroot
EXPOSE 8080 8081 9090
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s CMD ["/usr/local/bin/kourrier", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/kourrier"]
CMD ["serve"]
