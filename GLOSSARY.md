# Companion glossary

Use this glossary with the [setup and usage guide](README.md) and the [Pi runner contract](internal/pi/README.md).

| Term | Meaning in Companion |
| --- | --- |
| App | The Go service that handles Telegram messages and saves state. |
| App state | Data in the `state` volume. It includes pending requests, conversation history, and Telegram delivery records. |
| Pi RPC | The interface that the runner uses to send a prompt to Pi and receive events over standard input and output. |
| Durable workspace | The agent's writable working directory at `/workspace`. Only the runner mounts the `workspace` volume. Files survive container replacement. |
| Outbox | The saved queue of messages that the app has not finished sending to Telegram. |
| Pending request | A chat message that Companion still needs to finish. It remains in the saved queue until the app records a reply or an error. |
| Runner | The separate container that starts Pi and runs its tools. It does not mount app state. |
| Step | One Pi run for a chat request. A new attempt uses a fresh Pi process and the same workspace. |
| Workspace notes | Persistent files in `/workspace/notes` for preferences, decisions, and work progress. Ask Companion in chat to save, read, change, or remove notes. |
