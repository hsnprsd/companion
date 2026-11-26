# Companion architecture

Companion is a single-user service for one private Telegram chat. The Go app owns tasks, schedules, and message delivery. Codex runs one model step at a time in a separate container. It uses its native tools in a durable workspace. The Go app does not mount that workspace or handle file operations.

See [README.md](README.md) for setup and [GLOSSARY.md](GLOSSARY.md) for terms.

## 1. Components and deployment

```mermaid
flowchart LR
    User[Private Telegram chat] <--> TG[Telegram Bot API]
    TG <-->|HTTPS: getUpdates / sendMessage / setMessageReaction| App[Go app]
    App <-->|Loopback WebSocket: turns| Runner[Codex app-server]
    App <--> State[(State volume)]
    Config[Read-only Companion config] --> App
    Runner <--> Auth[(Codex sign-in volume)]
    Runner --> Shell[Sandboxed shell]
    Shell <--> Workspace[(Workspace volume)]
    Shell -->|Direct network requests| Network[Network]
    Runner <-->|Model requests and native search| OpenAI[OpenAI]
```

| Component | Responsibility | Source |
| --- | --- | --- |
| App entry point | Validate configuration and supervise the service loops. | [`main.go`](main.go), [`config.go`](config.go) |
| Telegram client | Poll for text updates from the configured private chat. Send plain text and reactions. | [`internal/telegram`](internal/telegram) |
| Engine | Handle commands, save state, select work, build prompts, and drain the outbox. | [`internal/engine`](internal/engine) |
| Codex client | Run one ephemeral app-server turn and return the final text and any reaction request. | [`internal/codex`](internal/codex) |

[Compose](compose.yaml) runs two services with separate filesystems. The app joins the runner's network namespace to reach `ws://127.0.0.1:4500`. Neither service publishes a port. The runner joins a Docker bridge with egress, which also lets the app reach Telegram. If you replace the runner, recreate the app so that it joins the new network namespace.

The [Dockerfile](Dockerfile) builds the Go `1.27.4` app and installs Codex CLI `0.172.0` and Bubblewrap in the runner. Both services run as UID/GID `1000:1000`, with read-only container roots, all Linux capabilities dropped, `no-new-privileges`, and no Docker socket. The runner uses the repository's [seccomp profile](docker/codex-seccomp.json), based on [Moby profiles at `a0fa708`](https://github.com/moby/profiles/blob/a0fa70805f742f7da15b367770d802ea2a86c68b/seccomp/default.json). Its additions let Bubblewrap create nested namespaces and set up their mount tree. The runner does not use privileged mode or unconfined seccomp. See [the upstream license](docker/MOBY-LICENSE).

| Data | Standard location | Access and lifetime |
| --- | --- | --- |
| Companion configuration | `/etc/companion/companion.json` | Read-only app bind mount. Contains the model ID, Telegram bot token, and private chat ID. |
| App state | `/app/state` | App-only named volume. Contains `state.json` and `state.lock`. |
| Durable workspace | `/workspace` | Runner-only named `workspace` volume. The agent can change files without approval. Files survive container replacement. |
| Codex sign-in | `/home/node/.codex` | Runner-only external volume `companion-codex-auth`. The shell cannot read it. |

## 2. Startup and process lifecycle

1. Load `model`, `tg-bot-token`, and `tg-chat-id` from the JSON config. The app limits the config to 4 KiB, validates the model and token formats, and requires a positive chat ID. It checks endpoint schemes and rejects credentials, query strings, and fragments in the URLs. See [Application settings](#application-settings) for the environment variables.
2. Create the state directory if necessary, acquire an exclusive `flock` on `state.lock`, and load the snapshot. A second app cannot open the same state directory while this lock is held. The app does not contact Codex until a step needs a model.
3. Start three loops. Telegram `Poll` accepts updates. Engine `Work` runs one model step at a time. Engine `Deliver` sends queued messages. The engine mutex serializes state changes. The loops do not hold it during network calls. Wake channels and timers let the loops wait for state changes or deadlines.

On `SIGINT` or `SIGTERM`, or when a loop exits, the supervisor cancels the shared context. It waits up to 15 seconds. Compose gives the app 30 seconds to stop. The worker stops selecting jobs and cancels the active model step. Cancellation does not undo changes to the workspace.

## 3. Durable state and commit boundary

The app uses one versioned JSON snapshot, not a database or a Codex session. [`state.go`](internal/engine/state.go) defines version `2` and validates the snapshot at load time. Version `1` is not supported. See the [upgrade instructions](README.md#upgrade-from-the-brokered-workspace-release).

| Record | Contents |
| --- | --- |
| Telegram cursor | Next update ID and time of the last saved update. |
| Tasks | Task ID, kind, instructions, status, revision, schedule, last result, any problem, and the Telegram message ID when available. |
| Conversation history | User text and the saved assistant reply for each conversation task. |
| Outbox and incidents | Queued text or reactions, confirmed text byte position, send-in-progress marker, retry time, and records of failed or uncertain deliveries. |

For each commit, the engine changes state under its mutex. It writes the snapshot to a temporary file in the state directory, syncs and closes the file, renames it to `state.json`, and syncs the directory. Only then does it publish the new Telegram cursor. A save error stops the engine and cancels the active step. The app does not acknowledge an update on the basis of a failed save.

A snapshot commit can include an update and all its state changes. It cannot include a Telegram send or a native tool action in the same transaction.

## 4. Telegram input and command routing

1. The Telegram client calls `getUpdates` with the saved offset, a 25-second long-poll timeout, and a batch limit of 25. It sorts updates by ID and skips non-positive IDs and IDs below the current cursor.
2. It only passes text and its Telegram message ID to the engine when `chat.id` matches the configured ID and `chat.type` is `private`. Other updates can advance the cursor but cannot create work.
3. The engine saves `update_id + 1` with the changes and replies from the update. The next poll uses that offset to acknowledge earlier updates. After six days without a saved update, the engine requests offset `0` because Telegram can start a new ID sequence.

Plain text creates a conversation task. The app handles slash commands locally:

| Input | Effect |
| --- | --- |
| `/help` | List commands. |
| `/work TEXT`, `/every DURATION TEXT` | Create a one-time or recurring task. |
| `/tasks`, `/task ID` | Inspect task state. `/tasks` omits completed conversation tasks. |
| `/change ID TEXT`, `/stop ID`, `/retry ID` | Replace task instructions, stop a task, or make a blocked task ready. |

The Telegram client uses a 40-second HTTP timeout and does not follow redirects. It hides transport error details that could contain the bot token. Polling retries transient errors and HTTP 409 with delays from 1 to 30 seconds, unless Telegram supplies a longer delay. Other permanent poll errors stop the service.

## 5. Task scheduling and memory

The worker selects ready tasks and due schedules. It starts after the last selected task ID and wraps to the lowest ID so that a recurring task cannot block newer work.

| Status | Meaning |
| --- | --- |
| `ready` | The worker can start a step. |
| `running` | A model step started. |
| `blocked` | A model error or timeout stopped the task. Use `/retry` to try again. |
| `complete` | A conversation or one-time task finished. |
| `scheduled` | A recurring task waits for its next due time. |
| `stopped` | The worker will not select the task. |

Recurring work uses Go duration syntax and a minimum interval of one second. The first run waits one interval. The app keeps the original cadence and skips missed ticks. Blank output or `NO_UPDATE` suppresses a recurring result message. Other results enter the outbox without a new Telegram update.

Each step has a five-minute timeout. The engine records `running` before it calls the runner. `/change`, `/stop`, and a successful `/retry` increment the task revision. When an update changes the active revision, the engine cancels the step and discards its result. It does not roll back native tool effects.

Each prompt includes the current request, task kind, the last task result if present, and up to 20 earlier conversation records.

Codex receives developer instructions to consult and maintain notes in `/workspace/notes` on its own initiative. It uses one Markdown file per topic with descriptive names and headings. It also reads, saves, changes, or removes notes when asked in ordinary conversation. The runner reads and writes these files with its native tools. The app does not inject the files into prompts. Removing a note does not erase conversation history. Older snapshots can contain a retired `notebook` field that the app preserves for manual recovery but does not send to Codex.

## 6. Codex step and protocol boundary

The [Codex client](internal/codex/client.go) opens one WebSocket per step. It sends `initialize` and `initialized`, checks Linux and `CODEX_HOME`, and looks for the exact configured model in the OpenAI `model/list` catalog. It requests a fresh ephemeral `thread/start` with provider fallback disabled. It checks the reported model, local environment, workspace roots, permission profile, approval policy, and that the shell has network access before sending `turn/start`.

The client uses JSON-RPC over WebSocket text messages. It sends one client request at a time and buffers a limited set of early turn notifications and reaction calls until it knows the turn ID. App-server runs native tools directly. The client accepts only the `telegram_react` server tool call and only for an active turn with a saved Telegram message ID. It rejects other server requests, including approval requests.

The tool accepts one supported emoji and no target parameters. The app uses the configured chat and the message that supplied the current request. A `/change` to a one-time task replaces the target with the command message. Recurring tasks and tasks without a saved message ID do not get the tool. A successful call records the request in memory, not in Telegram. The engine queues it only when it saves a successful result for the current revision.

The client returns the final assistant text, or the last assistant message if app-server did not mark a final answer. The agent can request a reaction without a text reply. The client does not stream partial text to Telegram.

The client rejects model changes and reroutes. It does not retry turns or select a fallback model. On cancellation or an early error with a known turn ID, it attempts `turn/interrupt` for up to one second. Neither an interrupt nor a WebSocket close proves that a native tool stopped. See the [runner contract](internal/codex/README.md) for protocol and account limits.

## 7. Workspace boundary

The agent's working directory and shell home are `/workspace`. It can use native tools to create, edit, rename, and delete its files. There is no file broker, Telegram file approval, or external alias. The app does not keep a log of each file operation.

The `companion-workspace` profile gives the shell network access, read access to minimal runtime files, and write access to `/workspace`. The profile does not expose the sign-in files directly, and the shell does not inherit the app-server environment. Network access lets the shell reach the unauthenticated loopback WebSocket and send requests directly to Codex without Companion’s checks. Codex treats the workspace project as untrusted. Keep app credentials, app state, host directories, and sockets out of the workspace. Back up files that the agent must not lose.

The shell boundary does not control app-server network requests. Native live web search can send queries and turn context to OpenAI. Account-side apps and plugins can operate outside the shell sandbox if they do not require client approval. Use an account without those integrations if they must not be available.

## 8. Telegram output and delivery

The outbox is first-in, first-out. The delivery loop splits messages at up to 4,096 UTF-16 code units without splitting a Unicode code point. It sends plain text with link previews disabled. It sends each reaction in one `setMessageReaction` request.

Before each text part or reaction, the loop saves `sending`. A Telegram message ID confirms text acceptance, and a `true` result confirms reaction acceptance. Neither result confirms that the user saw it. The engine advances the confirmed text byte position or removes the completed entry. On a definite temporary rejection, it saves an exponential retry delay of up to one minute, unless Telegram supplies a longer delay. A delayed entry holds its place in the queue.

On a permanent rejection or an unknown result, the engine records a delivery incident and replaces the rest of the entry with a notice. A disconnect, a timeout, or a restart with `sending` still set can make the result unknown. The engine does not resend the uncertain text part or reaction. If the notice also fails permanently or has an unknown result, the engine records the failure without generating a second notice. Use `/tasks` or `/task ID` to recover the last saved task result.

## 9. Shutdown and recovery

| Saved condition | Recovery behavior |
| --- | --- |
| An update and its effects were committed | Resume polling from the saved cursor. |
| A task was `running` | Make it `ready` and start a new ephemeral step. Partial output is not a checkpoint. The prompt tells the agent to check the workspace before it repeats work. |
| A text part or reaction was marked `sending` | Record an uncertain delivery and queue a notice. Resume other deliveries from their confirmed positions and saved retry times. |

The app does not provide exactly-once execution for model turns, native tools, or Telegram delivery. The state-volume lock prevents two engines from writing one snapshot. It does not coordinate separate deployments.

## 10. Operating limits and verification

- Use one app instance, one private chat, one Telegram poller, and one worker. There is no database server or state replication.
- Each commit rewrites the snapshot. The app does not remove old tasks, history, or delivery incidents. It sends up to 20 conversation records per prompt.
- Stop both services before backing up state and workspace. Protect both backups as private data. `docker compose down` keeps named volumes. `docker compose down -v` removes app state and workspace but not the external sign-in volume.

Run `go test -race ./...` for tests of configuration, Telegram HTTP behavior, scheduling, state, delivery, and the Codex protocol. Run [`tests/docker/run.sh`](tests/docker/run.sh) for the app image with fake Telegram and Codex peers. Run [`tests/docker/runner.sh`](tests/docker/runner.sh) for native file operations, workspace persistence, and shell boundary checks with real Codex and a local model fixture. Both Docker tests use disposable volumes and do not contact Telegram or OpenAI.

## Application settings

| Environment variable | Default outside Compose | Compose setting |
| --- | --- | --- |
| `COMPANION_CONFIG` | `companion.json` | `/etc/companion/companion.json`; change the host source with `COMPANION_CONFIG_SOURCE` |
| `COMPANION_STATE_DIR` | `./state` | `/app/state` |
| `COMPANION_CODEX_URL` | `ws://127.0.0.1:4500` | Same |
| `COMPANION_TELEGRAM_URL` | `https://api.telegram.org` | Same; override only for a trusted fixture |
