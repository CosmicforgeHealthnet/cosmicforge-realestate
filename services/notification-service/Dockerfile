# syntax=docker/dockerfile:1.7
# Production image for notification-service (api, worker, migrate binaries).
# Build context: repository root.
#   docker build -f services/notification-service/Dockerfile \
#     --build-arg VERSION=1.0.0 --build-arg COMMIT=$(git rev-parse HEAD) -t notification-service .
# Run: /app/api (default) | /app/worker | /app/migrate up
ARG GO_VERSION=1.26

FROM golang:${GO_VERSION}-alpine AS build
ENV CGO_ENABLED=0 GOWORK=off GOTOOLCHAIN=local GOFLAGS=-trimpath
WORKDIR /src
COPY shared/go/go.mod shared/go/go.sum shared/go/
COPY services/notification-service/go.mod services/notification-service/go.sum services/notification-service/
RUN --mount=type=cache,target=/go/pkg/mod cd services/notification-service && go mod download
COPY shared/go shared/go
COPY services/notification-service services/notification-service
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd services/notification-service && \
    BI=github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/buildinfo && \
    LDFLAGS="-s -w -X ${BI}.Version=${VERSION} -X ${BI}.Commit=${COMMIT} -X ${BI}.BuildTime=${BUILD_TIME}" && \
    go build -ldflags "$LDFLAGS" -o /out/api ./cmd/api && \
    go build -ldflags "$LDFLAGS" -o /out/worker ./cmd/worker && \
    go build -ldflags "$LDFLAGS" -o /out/migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/CosmicforgeHealthnet/cosmicforge-realestate" \
      org.opencontainers.image.title="notification-service"
COPY --from=build /out/ /app/
USER nonroot:nonroot
EXPOSE 8080 8081
HEALTHCHECK --interval=15s --timeout=5s --start-period=15s --retries=3 CMD ["/app/api", "healthcheck"]
ENTRYPOINT ["/app/api"]
