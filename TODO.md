# Companion: first version

Build the complete first version in [GOAL.md](GOAL.md). Verify the capabilities below together in Docker. Do not make one example task the release scope.

## 1. Set up the Go service

- [x] Add the Go service entry point and its Docker build. Run the service without root privileges.
- [x] Read `model`, `tg-bot-token`, and `tg-chat-id` from the local `companion.json` file or its mounted path. Provide a configuration template. Validate the inputs at startup. Do not log the token or copy the private file into the image.
- [x] Use persistent Docker storage for the workspace, task state, notebook, and approvals. Keep Codex sign-in in its existing volume. Do not expose the Telegram token or service state to the runner.

## 2. Connect the private Telegram chat

- [x] Use long polling. Accept messages and approval responses only from the configured private chat. Ignore other chats.
- [x] Accept conversation, new work, and requests to inspect, change, or stop existing work.
- [x] Save each accepted update before acknowledging it. Use the Telegram update ID to prevent duplicate tasks after a restart.

## 3. Run delegated work

- [x] Connect the Go service to [Codex app-server](https://developers.openai.com/codex/app-server). Request the configured model. Report an error if Codex cannot use it. Do not switch models automatically.
- [x] Track short tasks and ongoing responsibilities. Save progress at the end of each step. Record what is ready, in progress, blocked, or complete.
- [x] Run due work for ongoing responsibilities without a new message. Send useful feedback, results, and requests for decisions to the private chat.

## 4. Enforce access and approval

- [x] Enable the normal Codex tools, including shell commands in isolated runner scratch space and live OpenAI web search. Keep the durable workspace and granted external mounts out of the runner container. Enforce file access limits outside the model prompt.
- [x] Apply the permission rules before each brokered file action. Block actions that are not permitted. Do not let the runner grant access or bypass an approval.
- [x] Show the exact proposed action in Telegram when approval is required. Save the proposal and the user's decision. Execute only the action the user approved.
- [x] Let the user grant or revoke access without changing the application code. Start with limited permissions.

## 5. Remember and recover

- [x] Keep the notebook separate from task progress and the Codex session. Save useful preferences, decisions, and work context. Use relevant notes in later conversations.
- [x] Let the user inspect, correct, and delete notebook entries in Telegram.
- [x] On Docker shutdown, stop starting new work. Finish or cancel the current step within the shutdown period and keep the last saved progress.
- [x] Recover work, schedules, and pending approvals after restart. Limit each saved approval to the same proposed action. Check the result of an uncertain external action before trying it again. If the result cannot be checked, report the uncertainty.

## 6. Verify and document the release

- [x] Test chat filtering, duplicate updates, model selection, permission limits, approval decisions, and recovery with fake clients.
- [x] In Docker, verify conversation, a delegated task, an ongoing responsibility, a proactive update, notebook recall, and a blocked action that proceeds only after explicit approval.
- [x] Restart with work in progress and an approval pending. Verify that Companion continues without a new request and does not repeat a completed action.
- [x] Document how to configure, start, stop, restart, and use Companion.
