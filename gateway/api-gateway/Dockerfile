# syntax=docker/dockerfile:1.7
# Production image for api-gateway. Build context: repository root.
#   docker build -f gateway/api-gateway/Dockerfile \
#     --build-arg VERSION=1.0.0 --build-arg COMMIT=$(git rev-parse HEAD) \
#     --build-arg BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ) -t api-gateway .
ARG GO_VERSION=1.26

FROM golang:${GO_VERSION}-alpine AS build
ENV CGO_ENABLED=0 GOWORK=off GOTOOLCHAIN=local GOFLAGS=-trimpath
WORKDIR /src
COPY shared/go/go.mod shared/go/go.sum shared/go/
COPY gateway/api-gateway/go.mod gateway/api-gateway/go.sum gateway/api-gateway/
RUN --mount=type=cache,target=/go/pkg/mod cd gateway/api-gateway && go mod download
COPY shared/go shared/go
COPY gateway/api-gateway gateway/api-gateway
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd gateway/api-gateway && \
    BI=github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/buildinfo && \
    go build -ldflags "-s -w -X ${BI}.Version=${VERSION} -X ${BI}.Commit=${COMMIT} -X ${BI}.BuildTime=${BUILD_TIME}" \
      -o /out/api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/CosmicforgeHealthnet/cosmicforge-realestate" \
      org.opencontainers.image.title="api-gateway"
COPY --from=build /out/api /app/api
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 CMD ["/app/api", "healthcheck"]
# CMD (not ENTRYPOINT) so the same image runs any binary: docker run IMAGE /app/worker
CMD ["/app/api"]
