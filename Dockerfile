FROM golang:1.27.4-bookworm AS go-base
WORKDIR /src

FROM go-base AS app-build
COPY go.* *.go ./
COPY internal/ ./internal/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/companion .

FROM node:24-bookworm-slim AS runner
RUN apt-get update \
    && apt-get install -y --no-install-recommends bubblewrap ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && npm install --global --no-audit --no-fund @openai/codex@0.172.0 \
    && npm cache clean --force \
    && codex --version
ENV HOME=/home/node CODEX_HOME=/home/node/.codex
RUN install -d -m 0700 -o 1000 -g 1000 /home/node/.codex \
    && install -d -m 0700 -o 1000 -g 1000 /workspace
WORKDIR /workspace
USER 1000:1000
ENTRYPOINT ["codex", "app-server"]

FROM debian:bookworm-slim AS app
COPY --from=go-base /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=app-build /out/companion /usr/local/bin/companion
# Docker copies the ownership of these directories into new named volumes.
RUN install -d -m 0700 -o 1000 -g 1000 /app/state \
    && install -d -m 0755 /etc/companion
WORKDIR /app
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/companion"]
