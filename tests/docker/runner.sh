#!/bin/sh
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$root"
export RUNNER_PROBE_PROJECT=companion-runner-probe-$$

compose() {
    docker compose --env-file /dev/null -p "$RUNNER_PROBE_PROJECT" \
        -f compose.yaml -f tests/docker/runner.compose.yaml "$@"
}

cleanup() {
    rc=$?
    trap - EXIT
    compose down -v --remove-orphans --rmi local || rc=1
    exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

compose build codex
compose run --rm --no-deps -T -e PROBE_PHASE=create codex
compose run --rm --no-deps -T -e PROBE_PHASE=verify codex
