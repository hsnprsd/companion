FROM golang:1.27.4-bookworm AS fixture-build
WORKDIR /src
COPY go.* ./
COPY tests/*.go ./tests/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/fixture ./tests

FROM debian:bookworm-slim
COPY --from=fixture-build /out/fixture /usr/local/bin/fixture
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/fixture"]
