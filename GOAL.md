# Companion

## Goal

Build a personal companion for one person. Use one private Telegram chat to talk with Companion, give it work, and get updates. Companion should act like a colleague: remember what is in progress, give feedback, and take responsibility for work you delegate.

Companion should support different kinds of work, from a short request to an ongoing responsibility. It can start a conversation in Telegram when it finds a problem, needs a decision, or has useful feedback.

## Access and approval

Start with limited access, as you would for a new colleague. Give Companion access to more systems and permission to perform more actions as it earns your trust.

Keep access and approval separate. Access to a system does not mean Companion can make every change in it. When an action requires approval, Companion must tell you what it wants to do and wait for your explicit approval. Companion must not grant itself more access or remove an approval requirement.

## Memory

Give Companion two kinds of memory:

- **Working state:** what it is doing now, what it is waiting for, and what it plans to do next.
- **Notebook:** durable notes about your preferences, decisions, and work you have delegated. Use these notes to resume work and give feedback in later conversations.

## Implementation

- Write Companion in Go so you can read and maintain it.
- Run it in a Docker container on your machine for now.
- Give it an isolated workspace that it owns. Give it access to files or systems outside that workspace only when needed and explicitly granted.

## Agent runtime options

Use the existing ChatGPT subscription. Do not require a separately billed OpenAI API key for the first release. Use Codex app-server in a separate runner container. Read the exact model ID from the local `companion.json` file. Use [companion.example.json](companion.example.json) as a template. Let Codex use its normal tools, including shell commands in runner scratch space and live OpenAI web search.

Let the Go application own task state, the notebook, approvals, and Telegram messages. Do not make a runner session the only record of delegated work. Keep the durable workspace and all service files out of the runner container. Apply grants and approvals in the Go service before file actions in the durable workspace or on an external mount.

| Option | Benefit | Cost |
| --- | --- | --- |
| Run [Codex app-server](https://developers.openai.com/codex/app-server) as a separate service | Use OpenAI's ChatGPT sign-in and control the runner from Go through JSON-RPC. | Integrate with the Codex run lifecycle and verify that its tools fit Companion's permission rules. |
| Run Pi as a subprocess | Use Pi's ChatGPT sign-in and reuse its agent loop, tools, streaming, and context compaction through JSONL RPC. | Add a Node.js runtime and integrate with the Pi run lifecycle. |
| Write the loop in Go | Control each model call, tool call, pause, and retry. Use a Go model library where possible. | Find a supported model interface that uses the subscription. The standard OpenAI Go SDK uses separately billed API access. |

## Restart behavior

On a planned shutdown, stop starting new work. Save task progress and pending approvals before exit. After restart, recover the notebook and tasks. Keep pending approvals valid for the same proposed action. If the outcome of an external action is unknown, check its result before trying it again. If you cannot check the result, tell the user instead of repeating the action.

## First milestone

Build the first usable version of Companion. From one private Telegram chat, talk with it, delegate a task or an ongoing responsibility, and get updates and feedback without asking each time.

Companion must work in its own isolated workspace, keep task state and a durable notebook, and enforce the access you grant and the approvals you require. A container restart must preserve the notebook, work in progress, and pending approvals.

Verify these capabilities together in Docker. A single sample task does not define the milestone. Use the working version to decide which capabilities and integrations to add next.

## First release access

Use the runner's scratch space for shell work. Use the Go file tools for the durable workspace and for read-only external mounts. Require explicit approval for each durable workspace write and an explicit grant to read from each external mount. Use `/notes`, `/note`, and `/forget` to manage the notebook in Telegram.
