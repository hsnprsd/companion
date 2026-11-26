# Companion glossary

Use this glossary with the [setup and usage guide](README.md) and the [Codex runner contract](internal/codex/README.md).

| Term | Meaning in Companion |
| --- | --- |
| Action ID | The number that identifies a proposed file action. Use it with `/approve` or `/deny`. It is not the task ID. |
| App | The Go service that handles Telegram messages, schedules work, saves state, and enforces file access rules. |
| App state | Data that the app saves in the `state` volume. It includes task progress, the notebook, grants, approvals, and Telegram delivery records. |
| Approval | Permission to execute one exact proposed action. A workspace write needs approval for its path and content. A pending approval can survive an app restart. |
| Brokered file tool | A tool that Codex calls through the app: `list_files`, `read_file`, or `write_file`. The app checks access and applies approval rules before it operates on files. |
| Codex app-server | The process in the runner that accepts work from the app over a WebSocket and runs model turns. |
| Delegated task | One request for work that the user gives with `/work`. Use `/task` with its task ID to see its status. |
| Durable workspace | The file area in the app's `workspace` volume. Files survive container restarts. The standard deployment mounts this area only in the app container. |
| External alias | A name for a read-only directory mounted in the app. A path such as `@reference/guide.txt` uses the alias `reference`. Mounting a directory does not grant access to it. |
| Grant | Permission to list and read files under one external alias. Use `/grant NAME` to give it and `/revoke NAME` to remove it. A grant does not permit writes. |
| Notebook | A set of persistent notes, indexed by key, for preferences, decisions, and work context. Use `/notes`, `/note`, and `/forget` to manage them. |
| Outbox | The saved queue of messages that the app has not finished sending to Telegram. |
| Recurring task | Work that the user schedules with `/every`. The first run is due after one full interval. Later runs do not need new Telegram messages. |
| Runner | The separate container that runs Codex app-server. It does not store Companion's task progress, notebook, or approvals. |
| Runner scratch | The temporary writable `/scratch` directory for Codex shell commands. Runs share it while the runner is active. It is cleared when the runner stops. |
| Step | One model run within a task. A task can need more than one step, for example when the task pauses for a decision on a proposed write. |
| Task ID | The number that identifies a task. Use it with `/task`, `/change`, `/stop`, or `/retry`. |
| Uncertain write | A write that the app cannot confirm as applied or not applied. The app blocks the task and reports the problem instead of repeating the write. |
