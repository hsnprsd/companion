#!/bin/sh
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
export FIXTURE_DIR
FIXTURE_DIR=$(mktemp -d)
project=companion-integration-$$

compose() {
    docker compose --env-file /dev/null -p "$project" -f tests/compose.yaml "$@"
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

# Use only these synthetic files. Omit thinking-level to test the default.
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

printf '%s\n' 'Check filtering, deduplication, conversation, and reactions.'
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
send '/status'
wait_message 'Companion is idle.'
count_is turns 'hello-fixture' 1
count_is reactions '' 1

printf '%s\n' 'Check that the removed commands do not create work.'
for command in '/grant manual' '/revoke manual' '/access' '/approve 1' '/deny 1' '/note color amber' '/notes' '/forget color' '/work removed-fixture' \
    '/every 2s removed-fixture' '/tasks' '/task 1' '/change 1 text' '/stop 1' '/retry 1'; do
    send "$command"
done
wait_message 'Unknown command. Use /help to see the commands.' 15
count_is turns '' 1

printf '%s\n' 'Restart with an active request and a queued note request.'
send 'restart-fixture'
fx wait turns 'restart-fixture' 1
send 'Remember that I prefer short summaries.'
acked
count_is turns 'Remember that I prefer short summaries.' 0
send '/status'
wait_message 'Requests in queue: 1.'
app_id=$(compose ps -q app)
compose stop app
test "$(docker inspect --format '{{.State.ExitCode}}' "$app_id")" = 0
compose start app
# Do not send a Telegram update until both saved requests finish.
fx wait turns 'restart-fixture' 2
wait_message 'fixture resumed result'
wait_message 'fixture note request'
send '/help'
acked
wait_message 'Send a message to ask a question, request work, or manage notes.'
count_is turns 'Remember that I prefer short summaries.' 1
count_is messages 'fixture resumed result' 1

printf '%s\n' 'Check a reaction without a text reply.'
reaction_id=$((next + 1000))
send 'reaction-only-fixture'
acked
fx wait reactions "$reaction_id:👍" 1
count_is messages 'NO_REPLY' 0
count_is reactions '' 2

printf '%s\n' 'Check status, typing, and the progress reply during slow work.'
typing_before=$(fx count actions typing)
progress='I will inspect the fixture files.'
send 'slow-fixture'
wait_message "$progress"
send '/status'
wait_message 'Companion is working on a request.' 2
fx wait actions typing "$((typing_before + 2))"
wait_message 'fixture full answer'
count_is messages "$progress" 1
count_is turns 'slow-fixture' 1
send '/status'
wait_message 'Companion is idle.' 2

printf '%s\n' 'Docker integration passed.'
