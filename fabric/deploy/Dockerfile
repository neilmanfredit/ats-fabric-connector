# Builds the ats-fabric-connector Bullhorn runner: a scheduled job that
# invokes ingestr per entity, enforces the API budget, and rotates tokens.
# Telemetry is disabled at build and run time (build brief section 2.1.5).

FROM golang:1.23-bookworm AS build
WORKDIR /src

ENV INGESTR_DISABLE_TELEMETRY=true
ENV DISABLE_TELEMETRY=true
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/ingestr . && \
    go build -trimpath -ldflags="-s -w" -o /out/afc-runner ./fabric/runner

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app

ENV INGESTR_DISABLE_TELEMETRY=true
ENV DISABLE_TELEMETRY=true

COPY --from=build /out/ingestr /app/ingestr
COPY --from=build /out/afc-runner /app/afc-runner
COPY fabric/runner/entities.example.yaml /app/entities.example.yaml

USER nonroot:nonroot
ENTRYPOINT ["/app/afc-runner"]
