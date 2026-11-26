# Codex runner contract

Use Codex CLI `0.172.0`. The client opens one WebSocket connection and starts one ephemeral thread per `Run`. It checks the OpenAI model catalog, the selected thread model, and model-change notifications. It does not retry a turn or select a fallback model.

Only `read_file`, `list_files`, and `write_file` use the Companion callback. Companion must check paths and approvals. The client rejects other server requests, including requests to expand permissions. Codex built-in tool events do not use the callback.

## Deploy

- Run Codex as an unprivileged user in a separate Linux container. Use `/scratch` as its working directory. Mount `/scratch` as a writable tmpfs owned by the runner user. Do not mount the Companion workspace, service state, private config, Docker socket, or external file directories in the runner.
- Use `/home/node/.codex` for `CODEX_HOME`. Seed a dedicated, writable auth volume with the existing Codex sign-in only. Do not copy a user `config.toml`, skills, plugins, or MCP config into it. Do not inject other credentials into the runner. Use a read-only container root, drop all capabilities, and set `no-new-privileges`.
- Use a Docker seccomp profile that permits the namespace and mount calls required by Bubblewrap. The default Docker profile blocked the pinned sandbox. The repository's `docker/codex-seccomp.json` passed the offline probe. Do not replace the sandbox with full access to make shell commands work.
- Keep WebSocket traffic private to Companion. For the shared network namespace deployment, use `ws://127.0.0.1:4500` and do not publish that port. For a non-loopback listener, Codex requires WebSocket authentication. Set `--ws-auth capability-token --ws-token-file /absolute/path` on the server and `CODEX_WS_TOKEN_FILE` in Companion. Keep the token out of the URL.

Start app-server with this configuration from `/scratch`:

```sh
codex app-server --listen ws://127.0.0.1:4500 \
  --enable standalone_web_search \
  -c 'web_search="live"' \
  -c 'approval_policy="never"' \
  -c 'default_permissions="companion-scratch"' \
  -c 'permissions.companion-scratch.filesystem={":minimal"="read","/scratch"="write"}' \
  -c 'permissions.companion-scratch.network.enabled=false' \
  -c 'projects."/scratch".trust_level="untrusted"' \
  -c 'shell_environment_policy={inherit="none",set={HOME="/scratch",TMPDIR="/scratch",PATH="/usr/local/bin:/usr/bin:/bin"}}' \
  -c 'cli_auth_credentials_store="file"'
```

`:minimal` gives the shell read access to system binaries, libraries, and system configuration. It does not make `/home/node/.codex/auth.json` readable. A deny for the entire `CODEX_HOME` can prevent Codex from launching its sandbox helper. Keep app files and credentials out of the system read paths. Codex already protects project config paths such as `/scratch/.codex` from shell writes. Do not add explicit deny entries for absent scratch metadata directories: this version can fail to start Bubblewrap when it applies those entries.

## Limits

- Scratch is shared between runs in one runner and is cleared on runner restart. An ephemeral thread does not clear scratch. Project instructions in scratch can still affect model behavior. They cannot change Companion's broker checks.
- The sandbox controls shell processes, not app-server's own network requests. Native web search sends a query and can send turn context to OpenAI. `gpt-6-luna` uses Responses Lite. In the offline probe, `web_search="live"` alone did not expose search; `standalone_web_search` enabled OpenAI's native search path. It is not a third-party search integration or a dynamic Companion tool.
- No upstream features are disabled. The pinned version can synchronize a curated plugin catalog and use account-side app and plugin enablement. An auth-only volume does not clear cloud-side links. Use an account or workspace without linked apps or enabled remote plugins if they must not perform external actions. The shell profile and RPC rejection do not control hosted tools that need no client approval.
- Model checks apply to the requested root turn and the routing information app-server reports. They do not attest which backend model served tokens or control native sub-agent model choices.
- Cancellation requests a bounded, best-effort interrupt after app-server returns a turn ID. A disconnect can prevent the interrupt. Neither cancellation nor an error undoes a tool action.

## Verify

Run `go test -race ./internal/codex`. The tests use a local WebSocket fixture and fake credentials. The pinned-image probe used a local OpenAI fixture and confirmed the exact model request, file tool round trip, scratch writes, denial of direct and `/proc` credential reads, denial of shell TCP and permission escalation, and a native search request with `external_web_access=true`. The offline probe did not confirm production account entitlements.
