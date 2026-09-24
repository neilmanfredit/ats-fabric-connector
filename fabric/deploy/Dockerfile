# Builds the ats-fabric-connector Bullhorn runner: a scheduled job that
# invokes ingestr per entity, enforces the API budget, and rotates tokens.
# Telemetry is disabled at build and run time (build brief section 2.1.5).
#
# CGO is required (matches the upstream Dockerfile): pkg/source/adbc links
# against the Arrow ADBC driver manager via cgo, so a CGO_ENABLED=0 build
# fails with "undefined: drivermgr.Driver". The runtime image is therefore
# debian-slim with glibc, not a static distroless image.

FROM golang:1.26.7-bookworm AS build
WORKDIR /src

ENV INGESTR_DISABLE_TELEMETRY=true
ENV DISABLE_TELEMETRY=true
ENV CGO_ENABLED=1

RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc g++ libc6-dev \
    && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/ingestr . && \
    go build -trimpath -ldflags="-s -w" -o /out/afc-runner ./fabric/runner

FROM debian:bookworm-slim
WORKDIR /app

ENV INGESTR_DISABLE_TELEMETRY=true
ENV DISABLE_TELEMETRY=true

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --shell /usr/sbin/nologin afc

COPY --from=build /out/ingestr /usr/local/bin/ingestr
COPY --from=build /out/afc-runner /app/afc-runner
COPY fabric/runner/entities.example.yaml /app/entities.example.yaml

USER afc
ENTRYPOINT ["/app/afc-runner"]
