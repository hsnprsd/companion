# Companion

Use Companion in a private Telegram chat to ask questions, work with files, and save notes. It keeps pending requests, chat history, workspace files, and saved notes when you replace the containers.

## Use Companion in Telegram

| To do this | Send this |
| --- | --- |
| Ask a question | `What can you help me with?` |
| Request work | `Summarize the files in the workspace` |
| Check if Companion is working or idle | `/status` |
| See the commands | `/help` |

Companion handles requests in the order it receives them. If Companion restarts before it saves a result, it starts the pending request again. If Companion reports an error, check for partial file changes before you send the request again.

If Companion expects a request to take time, it can send a short update about what it plans to do. It continues working and sends the full answer separately. There is no fixed wait before an update. Send `/status` to see if Companion is working or if requests are in the queue.

Companion can save useful details and refer to them later without a request from you. To ask it to save a note, send `Remember that I prefer a short summary first.` To see saved notes, ask `What preferences did you save for me?` To remove a note, ask `Forget my preference for a short summary.` Removing a note does not delete chat history.

## Set up Companion

Use Docker Engine with Compose v2 or later. You need a Telegram bot, a private chat, and a model provider that Pi supports.

1. Create a bot with [BotFather](https://t.me/BotFather). Send your bot a private message. Use the Telegram Bot API's [`getUpdates` method](https://core.telegram.org/bots/api#getupdates) to find the positive number in `message.chat.id`. Do not use the same bot with another app that polls Telegram.
2. From this directory, create a private configuration file if you do not already have one:

   ```sh
   test -e companion.json || (umask 077; cp companion.example.json companion.json)
   chmod 600 companion.json
   ```

   Edit `companion.json`. Set `provider` to the Pi provider ID (the default is `openai`), `model` to the exact model ID available to your account, `tg-bot-token` to your bot token, and `tg-chat-id` to your private chat ID as a positive JSON integer. Both Telegram settings are required. Companion does not select a different model if yours is unavailable. Keep the file out of Git and shared directories. Make sure the app container's user (`1000:1000`, or the mapped host identity) can read it without making it public.
3. Create the Pi sign-in volume and open Pi. Run `/login`, select your provider, and follow its instructions. Exit Pi when sign-in is complete.

   ```sh
   docker volume create companion-pi-auth
   docker compose run --build --rm --entrypoint pi runner --no-session
   ```

   To see the models available to the runner, run `docker compose run --rm --entrypoint pi runner --list-models`.
4. Start Companion, then send `/status` to the bot to check that it replies:

   ```sh
   docker compose up --build -d
   ```

   If the bot does not reply, check `docker compose logs -f app runner`. Check the chat ID and confirm that no other app is polling the same bot. Send a plain text message to check that Pi can answer as well.

### Set the thinking level

Companion uses `medium` if you omit `thinking-level`. To change it, set `"thinking-level": "high"` in `companion.json`. Use a level that your model supports. Higher levels can take longer. Set `thinking-level` to `null` to let Pi choose the level.

To apply a change to a running installation, run `docker compose up -d --no-deps --force-recreate app`.

## Protect your files and data

- Companion can create, change, or delete files in `/workspace` without asking. It saves notes in `/workspace/notes`. Keep copies of files you cannot lose.
- Pi tools can make network requests and read the model credentials in the runner. Run only work you trust. Keep other secrets and host directories out of the runner.
- Back up both Docker volumes: `workspace` holds files and saved notes; `state` holds chat history, pending requests, and pending replies. Stop both services before you back up the volumes. Keep the backups private.

## Stop, start, or update Companion

| To do this | Run this |
| --- | --- |
| Stop both services without removing them | `docker compose stop` |
| Start the stopped services | `docker compose start` |
| Restart only the app | `docker compose restart app` |
| Rebuild after an update | `docker compose up --build -d` |

`docker compose down` keeps your files and app state. Do not add `-v` unless you want to delete both. The external Pi sign-in volume is not removed.

If you update from the Codex runner, use `docker compose down --remove-orphans` without `-v` before you start Pi. Sign in with Pi and check the model ID in `companion.json`. The switch keeps the existing workspace and app state. Pi does not use the old Codex sign-in volume.

If you update from an older development build, [check the state format before you start Companion](ARCHITECTURE.md#edit-old-development-state). Keep a private backup of your old state. To recover old notebook entries, find them in the backup and ask Companion to remember them again. Do not send secrets.

## Technical reference

See [Architecture](ARCHITECTURE.md) for application settings, deployment details, and tests. See the [glossary](GLOSSARY.md) for Companion terms and the [Pi runner contract](internal/pi/README.md) for protocol and access details.
