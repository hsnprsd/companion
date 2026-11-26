#!/bin/sh
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$root"
export FIXTURE_DIR
FIXTURE_DIR=$(mktemp -d)
project=companion-integration-$$

compose() {
    docker compose --env-file /dev/null -p "$project" -f tests/docker/compose.yaml "$@"
}

cleanup() {
    rc=$?
    trap - EXIT
    if [ "$rc" -ne 0 ]; then
        compose logs --no-color >&2 || true
        compose exec -T fixture /usr/local/bin/fixture status >&2 || true
    fi
    compose down -v --remove-orphans --rmi local || rc=1
    rm -rf -- "$FIXTURE_DIR"
    exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Use only these synthetic files. Do not resolve a production token or chat ID.
printf '%s\n' '{"model":"fixture-model-v1","tg-bot-token":"123456789:docker_fixture_token_123456789","tg-chat-id":9876543210}' \
    > "$FIXTURE_DIR/companion.json"
chmod 0444 "$FIXTURE_DIR/companion.json"
compose up --build --detach --wait --wait-timeout 30

fx() { compose exec -T fixture /usr/local/bin/fixture "$@"; }
app() { compose exec -T app "$@"; }
wait_message() { fx wait messages "$1" "${2:-1}"; }
count_is() {
    actual=$(fx count "$1" "$2")
    if [ "$actual" != "$3" ]; then
        printf >&2 'Expected %s %s containing <%s>; got %s\n' "$3" "$1" "$2" "$actual"
        exit 1
    fi
}

chat=9876543210
next=100
send_chat() {
    fx send "$next" "$1" "$2" "$3"
    next=$((next + 1))
}
send() { send_chat "$chat" private "$1"; }
acked() { fx offset "$next"; }

printf '%s\n' 'Check filtering, deduplication, conversation, reactions, and delegated work.'
test "$(app id -u)" = 1000
app test ! -e /workspace
send_chat 9876543211 private 'wrong-chat-fixture'
send_chat "$chat" supergroup 'wrong-type-fixture'
hello_id=$((next + 1000))
send 'hello-fixture'
acked
fx wait reactions "$hello_id:👍" 1
wait_message 'fixture conversation reply'
fx replay 102 "$chat" private 'hello-fixture'
delegate_id=$next
send '/work delegated-fixture'
acked
wait_message 'Task 2 is ready.'
wait_message 'fixture delegated result'
count_is turns 'hello-fixture' 1
count_is turns 'delegated-fixture' 1
count_is reactions '' 1

printf '%s\n' 'Check recurring work without a new Telegram update.'
send '/every 2s periodic-fixture'
acked
wait_message 'Task 3 runs every 2s.'
wait_message 'fixture proactive result' 2
send '/stop 3'
wait_message 'Task 3: stopped.'

printf '%s\n' 'Check that a note request reaches the runner as conversation.'
send 'Remember that I prefer short summaries.'
wait_message 'fixture note request'

printf '%s\n' 'Check that the removed commands do not create work.'
for command in '/grant manual' '/revoke manual' '/access' '/approve 1' '/deny 1' '/note color amber' '/notes' '/forget color'; do
    send "$command"
done
wait_message 'Unknown command. Use /help to see the commands.' 8

printf '%s\n' 'Restart with an active turn.'
send '/work restart-fixture'
acked
fx wait turns 'restart-fixture' 1
app_id=$(compose ps -q app)
compose stop app
test "$(docker inspect --format '{{.State.ExitCode}}' "$app_id")" = 0
compose start app
# Do not send a Telegram update until the active task resumes on its own.
fx wait turns 'restart-fixture' 2
wait_message 'fixture resumed result'
fx replay "$delegate_id" "$chat" private '/work delegated-fixture'
send '/help'
acked
wait_message 'Send a message to talk or manage notes.'
count_is turns 'delegated-fixture' 1
count_is messages 'fixture resumed result' 1

printf '%s\n' 'Check a reaction without a text reply.'
reaction_id=$((next + 1000))
send 'reaction-only-fixture'
acked
fx wait reactions "$reaction_id:👍" 1
count_is messages 'NO_REPLY' 0
count_is reactions '' 2

printf '%s\n' 'Docker integration passed.'
