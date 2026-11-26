# Codex runner contract

Use Codex CLI `0.172.0`. The client opens one WebSocket and starts one ephemeral thread per `Run`. It checks the OpenAI model catalog, the selected thread model, and model-change notifications. It does not retry a turn or select a fallback model. For a turn with a Telegram message target, the client registers `telegram_react`. It accepts one supported emoji, not a chat or message ID. The client verifies the thread and turn IDs before it accepts the call. It returns the emoji to the engine only after the turn succeeds. It rejects other server requests, including requests for more permissions.

## Deploy

- Use the runner settings in [compose.yaml](../../compose.yaml). Run Codex as an unprivileged user in a separate Linux container. Mount the named `workspace` volume at `/workspace`. Use that path as the working directory and shell home. Do not mount app state, private app configuration, host directories, or the Docker socket in the runner.
- Use `/home/node/.codex` for `CODEX_HOME`. Seed a dedicated, writable volume with the existing Codex sign-in only. Do not copy a user `config.toml`, skills, plugins, or MCP settings into it. Do not add other credentials to the runner. Use a read-only container root, drop all capabilities, and set `no-new-privileges`.
- Use the repository's [seccomp profile](../../docker/codex-seccomp.json) to permit the namespace and mount calls that Bubblewrap needs. Do not replace the shell sandbox with full container access.
- Use `ws://127.0.0.1:4500` in the shared network namespace. Do not publish that port. The Compose listener does not require authentication. Agent shell commands can reach it and send requests directly to Codex without Companion’s checks. For a non-loopback listener, Codex requires WebSocket authentication. Set `--ws-auth capability-token --ws-token-file /absolute/path` on the server and `CODEX_WS_TOKEN_FILE` in Companion. Keep the token out of the URL.

Compose defines the `companion-workspace` profile. It gives the shell network access, read access to minimal runtime files, and write access to `/workspace`. The profile does not expose the sign-in volume directly. Codex treats the workspace as an untrusted project and keeps its default protections for project metadata. Keep app files and secrets out of the workspace and system read paths.

## Limits

- Workspace changes survive runner replacement. A new thread does not clear the workspace. Project instructions there can affect model behavior but do not set the shell permission profile.
- The shell sandbox does not control app-server's own network requests. Native live web search runs through OpenAI and can send turn context. The client enables `standalone_web_search` for the Responses Lite tool path.
- The pinned CLI can use account-side apps and plugins. An auth-only volume does not remove cloud-side links. Use an account without linked apps or enabled remote plugins if those tools must not perform external actions. The shell sandbox does not control hosted tools that need no client approval.
- Model checks apply to the requested root turn and the routing information that app-server reports. They do not attest which backend model served tokens or control native sub-agent model choices.
- Cancellation requests a best-effort interrupt after app-server returns a turn ID. A disconnect can prevent the interrupt. Cancellation does not undo a tool action.

## Verify

Run `go test -race ./internal/codex` for the WebSocket contract. Run [`tests/docker/runner.sh`](../../tests/docker/runner.sh) from the repository root to test real Codex with a local model fixture. The probe checks native file operations without approvals, workspace persistence across runner replacement, HTTP access over the Docker bridge, and that the shell cannot directly read sign-in files. It uses disposable volumes. It does not contact OpenAI or check production account access.
