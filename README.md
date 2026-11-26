# Companion

Use Companion in a private Telegram chat to ask questions, assign tasks, and schedule recurring checks. Companion can send results without a new message from you. It keeps your tasks, workspace files, and saved notes when you replace the containers.

## Use Companion in Telegram

| To do this | Send this |
| --- | --- |
| Ask a question | `What can you help me with?` |
| Assign one task | `/work Summarize the files in the workspace` |
| Schedule a recurring check | `/every 24h Check the workspace and tell me what changed` |
| Find a task ID or see a result | `/tasks` or `/task ID` |
| Change the instructions and run the task again | `/change ID New instructions` |
| Stop a task or schedule | `/stop ID` |
| Retry a blocked task | `/retry ID` |
| See the commands | `/help` |

Replace `ID` with the number Companion gives the task. A recurring task first runs after one full interval. Companion can skip the message if there is nothing to report. Before you retry a task, check whether the earlier attempt changed any files. Stopping a task does not undo its changes.

Companion can save useful details and refer to them later without a request from you. To ask it to save a note, send `Remember that I prefer a short summary first.` To see saved notes, ask `What preferences did you save for me?` To remove a note, ask `Forget my preference for a short summary.` Removing a note does not delete chat history.

Companion can react to a chat message or a one-time work request, but not to an older message or a recurring task. Try `React to this message with 👍 and do not send a text reply.`

## Set up Companion

Use Docker Engine with Compose v2 or later on a Linux host or Docker VM that permits nested user namespaces for the Codex sandbox. You need a Telegram bot, a private chat, and a Codex sign-in from your ChatGPT subscription. You do not need an OpenAI API key.

1. Create a bot with [BotFather](https://t.me/BotFather). Send your bot a private message. Use the Telegram Bot API's [`getUpdates` method](https://core.telegram.org/bots/api#getupdates) to find the positive number in `message.chat.id`. Do not use the same bot with another app that polls Telegram.
2. From this directory, create a private configuration file if you do not already have one:

   ```sh
   test -e companion.json || (umask 077; cp companion.example.json companion.json)
   chmod 600 companion.json
   ```

   Edit `companion.json`. Set `model` to the exact Codex model ID available to your subscription, `tg-bot-token` to your bot token, and `tg-chat-id` to your chat ID as a JSON number. Companion does not select a different model if yours is unavailable. Keep the file out of Git and shared directories. Make sure the app container's user (`1000:1000`, or the mapped host identity) can read it without making it public.
3. Make sure the Docker volume `companion-codex-auth` contains a valid Codex sign-in. Use this volume for sign-in only. If the volume is missing or Companion cannot use it, see the [Codex sign-in requirements](internal/codex/README.md#codex-sign-in).
4. Start Companion, then send `/help` to the bot to check that it replies:

   ```sh
   docker compose up --build -d
   ```

   If the bot does not reply, check `docker compose logs -f app codex`. Check the chat ID and confirm that no other app is polling the same bot. Send a plain text message to check that Codex can answer as well.

## Protect your files and data

- Companion can create, change, or delete files in `/workspace` without asking. It saves notes in `/workspace/notes`. Keep copies of files you cannot lose.
- Companion can make network requests. Keep secrets out of the workspace. Do not mount host directories in it. Companion does not mount them by default.
- Back up both Docker volumes: `workspace` holds files and saved notes; `state` holds tasks, chat history, and pending Telegram replies. Stop both services before you back up the volumes. Keep the backups private.

## Stop, start, or update Companion

| To do this | Run this |
| --- | --- |
| Stop both services without removing them | `docker compose stop` |
| Start the stopped services | `docker compose start` |
| Restart only the app | `docker compose restart app` |
| Rebuild after an update | `docker compose up --build -d` |

If you replace the Codex container separately, recreate the app container too. `docker compose down` keeps your files and app state. Do not add `-v` unless you want to delete both. The external Codex sign-in volume is not removed.

### Upgrade from the brokered workspace release

Companion keeps your workspace files but cannot load version 1 app state. Stop both services and back up the old state before you continue. The reset below discards tasks, schedules, old notebook entries, and queued replies. Companion does not run old approvals. Check any file that Companion was writing when you stopped it. Telegram can still deliver messages that the old app had not acknowledged.

Remove any Compose override that adds external aliases. For the default Compose project name, run:

```sh
docker compose down
docker volume rm companion_state
docker compose up --build -d
```

If you use a different project name, use that project's state volume. Do not use `docker compose down -v`: it also deletes the workspace.

### Recover notes saved with `/note`

Use plain text instead of `/note`, `/notes`, and `/forget`. If you have version 2 state with old notebook entries, Companion keeps those entries in the `notebook` field of `state.json` but does not use them. To keep an old note active, find it in a private copy of the state file and ask Companion to remember it again. Do not send secrets.

## Technical reference

See [Architecture](ARCHITECTURE.md) for application settings, deployment details, and tests. See the [glossary](GLOSSARY.md) for Companion terms and the [Codex runner contract](internal/codex/README.md) for sign-in and sandbox details.
