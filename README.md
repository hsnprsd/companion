# Companion

Companion accepts work and sends updates in one private Telegram chat. It keeps tasks, the notebook, and workspace files across container restarts. See [ARCHITECTURE.md](ARCHITECTURE.md) for the system design and [GLOSSARY.md](GLOSSARY.md) for definitions of Companion terms.

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

A recurring task first runs after its interval. Companion can send a result without a new Telegram message. Companion can change or delete files in its workspace without asking. Stopping a task does not undo file changes.

Companion can react to a chat message or a one-time work request, but not to an older message or a recurring task. Try `React to this message with 👍 and do not send a text reply.`

## Access and storage

- **Durable workspace:** The runner mounts the named `workspace` volume at `/workspace`. This is the agent's working directory and shell home. The agent can use native tools there without file approvals. The Go app does not mount this volume.
- **App state:** The app alone mounts the named `state` volume. This volume holds tasks, the notebook, the Telegram cursor, and the outbox. Stop both services before you back up these volumes. Protect backups as private data.
- **Shell access:** The Codex shell can read runtime files, write in `/workspace`, and make direct network requests. The shell profile does not grant access to Codex sign-in files. It does not limit network requests to specific hosts. Do not put secrets in the workspace.
- **Container boundary:** Both services run without root, with separate filesystems, read-only container roots, and no Docker socket. App-server listens only on loopback. Docker publishes no port. The app and the agent's shell can both reach this unauthenticated listener. The shell can send requests directly to Codex without Companion’s checks. Do not attach unrelated containers to the runner’s network namespace. Companion does not mount host directories for the agent by default.

The runner uses [a Docker seccomp profile](docker/codex-seccomp.json) based on [Moby profiles at `a0fa708`](https://github.com/moby/profiles/blob/a0fa70805f742f7da15b367770d802ea2a86c68b/seccomp/default.json). Its additions let bubblewrap create nested namespaces and set up their mount tree. The outer container drops all capabilities and uses `no-new-privileges`; it does not use privileged mode or unconfined seccomp. See [the upstream license](docker/MOBY-LICENSE).

### Upgrade from the brokered workspace release

Remove any Compose override that adds external aliases. The new runner uses the existing `workspace` volume. The app does not read version 1 state snapshots. Stop both services and back up the old state if you need its records. Then reset only the `state` volume. This discards tasks, schedules, notes, the Telegram cursor, and queued replies. Old approval records are not executed. Telegram can still deliver updates that the old app did not acknowledge. Check any file that was being written when the old app stopped.

For the default Compose project name, run:

```sh
docker compose down
docker volume rm companion_state
docker compose up --build -d
```

If you use a different project name, use the state volume for that project. Do not use `docker compose down -v`: it also removes the workspace volume.

### Application settings

| Environment variable | Default outside Compose | Compose setting |
| --- | --- | --- |
| `COMPANION_CONFIG` | `companion.json` | `/etc/companion/companion.json`; change the host source with `COMPANION_CONFIG_SOURCE` |
| `COMPANION_STATE_DIR` | `./state` | `/app/state` |
| `COMPANION_CODEX_URL` | `ws://127.0.0.1:4500` | Same |
| `COMPANION_TELEGRAM_URL` | `https://api.telegram.org` | Same; override only for a trusted fixture |

## Verify the build

```sh
go test -race ./...
tests/docker/run.sh
tests/docker/runner.sh
```

The app test uses fake Telegram and Codex peers. It checks update filtering, duplicate updates, conversation, reactions, delegated and recurring work, notebook recall, removed commands, and restart during active work. The runner probe uses real Codex with a local model fixture. It checks the reaction tool protocol. It also checks that native shell tools can change workspace files without approval, that the files survive runner replacement, and that the shell can reach a local HTTP fixture but cannot directly read sign-in files. Both tests use disposable volumes and do not contact Telegram or OpenAI.
