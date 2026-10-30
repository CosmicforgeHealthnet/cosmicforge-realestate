# syntax=docker/dockerfile:1.7
ARG GO_VERSION=1.26.10
FROM golang:${GO_VERSION}-alpine AS build
ENV CGO_ENABLED=0 GOWORK=off GOTOOLCHAIN=local GOFLAGS=-trimpath
WORKDIR /src
COPY shared/go/go.mod shared/go/go.sum shared/go/
COPY services/business-operations-service/go.mod services/business-operations-service/go.sum services/business-operations-service/
RUN --mount=type=cache,target=/go/pkg/mod cd services/business-operations-service && go mod download
COPY shared/go shared/go
COPY services/business-operations-service services/business-operations-service
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd services/business-operations-service && \
    BI=github.com/CosmicforgeHealthnet/cosmicforge-realestate/shared/go/buildinfo && \
    LDFLAGS="-s -w -X ${BI}.Version=${VERSION} -X ${BI}.Commit=${COMMIT} -X ${BI}.BuildTime=${BUILD_TIME}" && \
    go build -ldflags "$LDFLAGS" -o /out/api ./cmd/api && \
    go build -ldflags "$LDFLAGS" -o /out/worker ./cmd/worker && \
    go build -ldflags "$LDFLAGS" -o /out/migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/CosmicforgeHealthnet/cosmicforge-realestate" \
      org.opencontainers.image.title="business-operations-service"
COPY --from=build /out/ /app/
COPY --from=build /src/services/business-operations-service/migrations /app/migrations
USER nonroot:nonroot
EXPOSE 8080 8081
HEALTHCHECK --interval=15s --timeout=5s --start-period=15s --retries=3 CMD ["/app/api", "healthcheck"]
CMD ["/app/api"]
