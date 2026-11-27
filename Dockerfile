FROM golang:1.27.4-bookworm AS go-base
WORKDIR /src

FROM go-base AS app-build
COPY go.* *.go ./
COPY internal/ ./internal/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/companion .

FROM node:24-bookworm-slim AS runner
RUN npm install --global --no-audit --no-fund @earendil-works/pi-coding-agent@1.6.1 \
    && npm cache clean --force \
    && pi --version
ENV HOME=/home/node PI_CODING_AGENT_DIR=/home/node/.pi/agent PI_SKIP_VERSION_CHECK=1
RUN install -d -m 0700 -o 1000 -g 1000 /home/node/.pi/agent /workspace /run/companion
COPY --from=app-build /out/companion /usr/local/bin/companion
COPY internal/pi/send-message.ts /usr/local/lib/node_modules/@earendil-works/pi-coding-agent/companion-send-message.ts
WORKDIR /workspace
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/companion"]
CMD ["runner"]

FROM debian:bookworm-slim AS app
COPY --from=go-base /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=app-build /out/companion /usr/local/bin/companion
# Docker copies the ownership of these directories into new named volumes.
RUN install -d -m 0700 -o 1000 -g 1000 /app/state /run/companion \
    && install -d -m 0755 /etc/companion
WORKDIR /app
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/companion"]
