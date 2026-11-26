# Companion

Companion accepts work and sends updates in one private Telegram chat. It keeps tasks, the notebook, approvals, and files across container restarts. See [GOAL.md](GOAL.md) for the project goal and [GLOSSARY.md](GLOSSARY.md) for definitions of Companion terms.

## Set up

Use Docker Engine with Compose v2 or later. Use a Linux host or Docker VM that permits nested user namespaces for Codex's bubblewrap sandbox. Docker builds the app with Go `1.27.4` and installs Codex CLI `0.172.0` in a separate image.

1. Create a Telegram bot through BotFather. Get the positive numeric ID of the private chat you want to use. Do not share the bot with other polling services.
2. If `companion.json` does not exist, copy the template. Edit the local configuration in a text editor. Set `model` to the exact Codex model ID available to your ChatGPT subscription, `tg-bot-token` to the bot token, and `tg-chat-id` to the chat ID as a JSON number. Companion does not select a fallback model.

   ```sh
   test -e companion.json || (umask 077; cp companion.example.json companion.json)
   chmod 600 companion.json
   ```

   Keep `companion.json` out of Git, build contexts, logs, and shared directories. The app runs as UID/GID `1000:1000`. Make sure this container identity can read the file without making it public. On a host with UID remapping, use the mapped identity. Only the app mounts this file, read-only.
3. Use the existing signed-in Docker volume `companion-codex-auth`. Keep this volume for Codex sign-in. Do not put unrelated keys, integrations, or configuration in it. No OpenAI API key is necessary.

   If sign-in or volume access fails, check ownership and modes without reading the files:

   ```sh
   docker volume inspect --format '{{.Name}} {{.Driver}}' companion-codex-auth
   docker run --rm --network none --user 0:0 \
     --mount type=volume,source=companion-codex-auth,target=/inspect,readonly,volume-nocopy \
     --entrypoint stat debian:bookworm-slim \
     -c '%n uid=%u gid=%g mode=%a' /inspect /inspect/auth.json
   ```

   Use this check only after `docker volume inspect` confirms that the volume exists. The runner uses UID/GID `1000:1000`. The expected directory and file modes are `700` and `600`, respectively. Do not recursively change permissions or ownership on an existing sign-in volume. If the owner does not match, use a verified sign-in procedure for the runner identity instead of copying or exposing credential files.
4. Start Companion from this directory:

   ```sh
   docker compose up --build -d
   docker compose logs -f app codex
   ```

   Send `/help` in the configured private chat to confirm setup. Use `docker compose stop` to stop both containers. Use `docker compose start` to start them again or `docker compose restart app` to restart only the app. Rebuild with `docker compose up --build -d` after a source change. If you replace the runner container, recreate the app too so that it joins the new runner's network namespace.

   `docker compose down` removes the containers and network but keeps the named state and workspace volumes. Do not use `down -v` unless you want to delete the app's state and workspace. Compose does not remove the external sign-in volume.

## Use Telegram

| To do this | Send |
| --- | --- |
| Talk with Companion | A plain text message |
| Delegate one task | `/work Summarize the files in the workspace` |
| Delegate recurring work | `/every 24h Check the workspace and report useful changes` |
| Inspect work | `/tasks` or `/task ID` |
| Change, stop, or retry work | `/change ID New instructions`, `/stop ID`, or `/retry ID` |
| Add or replace a notebook entry | `/note format Use a short summary first` |
| Inspect or delete an entry | `/notes` or `/forget format` |
| See or change external read access | `/access`, `/grant NAME`, or `/revoke NAME` |

A recurring task first runs after its interval. Companion can send a result without a new Telegram message. If a task asks to write a workspace file, check the exact path and text in the approval message. Send the exact `/approve ID` or `/deny ID` shown in that message. An approval authorizes only that proposal. Pending approvals remain valid across a restart. If Companion cannot verify an interrupted write, it reports the uncertainty instead of repeating it.

## Access and storage

- **Durable workspace:** Companion can list and read workspace files by relative path. Every durable write goes through the Go app and needs explicit approval. The named `workspace` volume belongs to the app.
- **App state:** The separate named `state` volume holds task progress, the notebook, approvals, and Telegram delivery state. Stop the app before you back up these volumes. Protect backups as private data.
- **Runner scratch:** Codex uses its normal feature defaults. Native shell commands run in a sandbox with read-only runtime files, a writable `/scratch`, and no shell network access. `/scratch` is temporary and does not contain the app workspace. It is cleared when the runner stops. The runner never approves a sandbox expansion. Native live web search uses OpenAI and can send search queries there; it is not shell network access. No other integrations are configured by Companion.
- **Container boundary:** Both services run without root, with separate filesystems and no Docker socket. The runner mounts only the external Codex volume as persistent storage. The shell profile does not expose sign-in credentials. App-server listens only on loopback. The app shares the runner's network namespace to reach that listener. Docker publishes no port. Do not attach unrelated containers to this namespace.

The runner uses [a Docker seccomp profile](docker/codex-seccomp.json) based on [Moby profiles at `a0fa708`](https://github.com/moby/profiles/blob/a0fa70805f742f7da15b367770d802ea2a86c68b/seccomp/default.json). Its additions let bubblewrap create nested namespaces and set up their mount tree. The outer container drops all capabilities and uses `no-new-privileges`; it does not use privileged mode or unconfined seccomp. See [the upstream license](docker/MOBY-LICENSE).

### Optional external reads

Mount only directories you want Companion to be able to read after you grant access. Define each alias as one direct subdirectory of the external root. For example, add `compose.external.yaml`:

```yaml
services:
  app:
    environment:
      COMPANION_EXTERNAL_DIR: /app/external
    volumes:
      - type: bind
        source: /absolute/path/to/reference-files
        target: /app/external/reference
        read_only: true
        bind: {create_host_path: false, selinux: Z}
```

Run `docker compose -f compose.yaml -f compose.external.yaml up -d`. Send `/grant reference` to permit reads such as `@reference/guide.txt`. Use `/revoke reference` to remove the grant. Companion cannot add a mount or write to an external alias. Do not mount the app config, private keys, or unrelated files under this root. Use the same Compose files for later lifecycle commands.

### Application settings

| Environment variable | Default outside Compose | Compose setting |
| --- | --- | --- |
| `COMPANION_CONFIG` | `companion.json` | `/etc/companion/companion.json`; change the host source with `COMPANION_CONFIG_SOURCE` |
| `COMPANION_STATE_DIR` | `./state` | `/app/state` |
| `COMPANION_WORKSPACE` | `./workspace` | `/app/workspace` |
| `COMPANION_CODEX_URL` | `ws://127.0.0.1:4500` | Same |
| `COMPANION_TELEGRAM_URL` | `https://api.telegram.org` | Same; override only for a trusted fixture |
| `COMPANION_EXTERNAL_DIR` | Unset | Unset unless you add external mounts |

## Verify the build

```sh
go test -race ./...
tests/docker/run.sh
```

The test builds the real app image and runs it against fake Telegram and fake Codex WebSocket services. It uses a Docker network with no external access, a generated config, and disposable volumes. It does not read the local config, mount the real Codex volume, or contact Telegram. It checks filtering, stale updates, conversation, delegated and recurring work, notebook changes and recall, external access grants, exact write approval, and restart with active work and another approval pending. The test checks that the completed write is not repeated.
