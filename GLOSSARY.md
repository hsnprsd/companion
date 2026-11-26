# Companion glossary

Use this glossary with the [setup and usage guide](README.md) and the [Codex runner contract](internal/codex/README.md).

| Term | Meaning in Companion |
| --- | --- |
| App | The Go service that handles Telegram messages, schedules work, and saves state. |
| App state | Data in the `state` volume. It includes tasks, conversation history, and Telegram delivery records. |
| Codex app-server | The process in the runner that accepts work from the app over a WebSocket and runs model turns. |
| Delegated task | One request for work that the user gives with `/work`. Use `/task` with the task ID to see its status. |
| Durable workspace | The agent's writable home at `/workspace`. Only the runner mounts the `workspace` volume. Files survive container replacement. |
| Outbox | The saved queue of text and reactions that the app has not finished sending to Telegram. |
| Recurring task | Work that the user schedules with `/every`. The first run is due after one full interval. Later runs do not need new Telegram messages. |
| Runner | The separate container that runs Codex app-server and its native tools. It does not mount app state. |
| Step | One model run for a task. A new attempt uses a new Codex thread and the same workspace. |
| Task ID | The number that identifies a task. Use it with `/task`, `/change`, `/stop`, or `/retry`. |
| Workspace notes | Persistent files in `/workspace/notes` for preferences, decisions, and work progress. Ask Companion in chat to save, read, change, or remove notes. |
