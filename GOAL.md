# Companion

## Goal

Build a personal companion for one person. Use one private Telegram chat to talk with Companion, give it work, and get updates. Companion should act like a colleague: remember what is in progress, give feedback, and take responsibility for work you delegate.

Companion should support different kinds of work, from a short request to an ongoing responsibility. It can start a conversation in Telegram when it finds a problem, needs a decision, or has useful feedback.

## Access and approval

Give Companion control of its own workspace. Do not require approval for work there. Keep the host, service credentials, and app state outside the agent's reach. Add access to other systems only when there is a specific need. If a future integration needs approval, enforce that approval at the integration boundary.

## Memory

Give Companion two kinds of memory:

- **Working state:** what it is doing now, what it is waiting for, and what it plans to do next.
- **Notebook:** durable notes about your preferences, decisions, and work you have delegated. Use these notes to resume work and give feedback in later conversations.

## Implementation

- Write Companion in Go so you can read and maintain it.
- Run it in a Docker container on your machine for now.
- Give the runner an isolated, durable workspace that the agent owns. Do not mount host files for the agent by default.

## Agent runtime options

Use the existing ChatGPT subscription. Do not require a separately billed OpenAI API key for the first release. Use Codex app-server in a separate runner container. Read the exact model ID from the local `companion.json` file. Use [companion.example.json](companion.example.json) as a template. Let Codex use its native tools, including shell commands in the workspace and live OpenAI web search.

Let the Go app own task state, the notebook, and Telegram messages. Do not make a runner session the only record of delegated work. Mount the durable workspace only in the runner. Keep service files out of the agent's shell.

| Option | Benefit | Cost |
| --- | --- | --- |
| Run [Codex app-server](https://developers.openai.com/codex/app-server) as a separate service | Use OpenAI's ChatGPT sign-in and control the runner from Go through JSON-RPC. | Integrate with the Codex run lifecycle and verify that its tools fit Companion's permission rules. |
| Run Pi as a subprocess | Use Pi's ChatGPT sign-in and reuse its agent loop, tools, streaming, and context compaction through JSONL RPC. | Add a Node.js runtime and integrate with the Pi run lifecycle. |
| Write the loop in Go | Control each model call, tool call, pause, and retry. Use a Go model library where possible. | Find a supported model interface that uses the subscription. The standard OpenAI Go SDK uses separately billed API access. |

## Restart behavior

On a planned shutdown, stop starting new work. After restart, recover the notebook and tasks. Keep workspace files across runner replacements. A stopped turn can leave file changes. Inspect the workspace before you repeat interrupted work.

## First milestone

Build the first usable version of Companion. From one private Telegram chat, talk with it, delegate a task or an ongoing responsibility, and get updates and feedback without asking each time.

Companion must work in its own isolated workspace and keep a durable record of tasks and notes. A container restart must preserve the notebook, tasks, and files.

Verify these capabilities together in Docker. A single sample task does not define the milestone. Use the working version to decide which capabilities and integrations to add next.

## First release access

Use native tools in the runner's workspace. Let shell commands make direct network requests. Do not mount host directories or sockets for the agent by default. Use `/notes`, `/note`, and `/forget` to manage the notebook in Telegram.
