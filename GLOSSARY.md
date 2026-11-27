# Companion glossary

Use this glossary with the [setup and usage guide](README.md) and the [Codex runner contract](internal/codex/README.md).

| Term | Meaning in Companion |
| --- | --- |
| App | The Go service that handles Telegram messages and saves state. |
| App state | Data in the `state` volume. It includes pending requests, conversation history, and Telegram delivery records. |
| Codex app-server | The process in the runner that accepts work from the app over a WebSocket and runs model turns. |
| Durable workspace | The agent's writable home at `/workspace`. Only the runner mounts the `workspace` volume. Files survive container replacement. |
| Outbox | The saved queue of messages that the app has not finished sending to Telegram. |
| Pending request | A chat message that Companion still needs to finish. It remains in the saved queue until the app records a reply or an error. |
| Runner | The separate container that runs Codex app-server and its native tools. It does not mount app state. |
| Step | One model run for a chat request. A new attempt uses a new Codex thread and the same workspace. |
| Workspace notes | Persistent files in `/workspace/notes` for preferences, decisions, and work progress. Ask Companion in chat to save, read, change, or remove notes. |
