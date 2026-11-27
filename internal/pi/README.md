# Pi runner contract

[Compose](../../compose.yaml) runs `companion runner` in the Pi container. It starts Pi `1.6.1` in `/workspace` with no disk session. Go owns conversation history. See [Pi RPC](https://pi.dev/docs/rpc) for the subprocess protocol.

## Step lifecycle

1. The app creates `/run/companion/msg-<id>.sock` with a new random step ID. It posts the ID, prompt, provider, model, and thinking level to `/run` on `/run/companion/runner.sock`.
2. The runner starts one Pi subprocess. It loads the `send_message` extension from the image and disables discovered extensions, skills, prompt templates, context files, and MCP. It passes the message socket path in `COMPANION_MESSAGE_SOCKET`.
3. The runner checks Pi's provider and model with `get_state`, turns off automatic model retry, and sends `prompt`. It reads LF-delimited RPC records and waits for both a successful `prompt` response with `disposition: started` and an `agent_settled` event. It does not forward assistant text. A model error, abort, output limit, or early process exit fails the step.
4. The runner stops and reaps Pi before it replies. It also signals the process group. On cancellation, this does not undo tool actions or guarantee that detached processes stop. The app closes and unlinks the message socket when the step ends.

## `send_message`

The extension runs its tool calls in sequence. Each call posts `{"text":"message"}` to `/message` on the step's callback socket. The app accepts non-blank text up to 64 KiB of UTF-8. It invokes the active `engine.Runner` message callback and returns HTTP 204 only after the outbox commit. The extension returns `queued` only on HTTP 204. This confirms the save, not Telegram delivery.

The extension does not retry a failed callback. A lost response can leave a saved message even though Pi did not receive `queued`. Canceling or failing a step does not remove replies that the app has already queued.

## Access and sign-in

Follow [setup](../../README.md#set-up-companion) to sign in from the runner image. The volume `companion-pi-auth` holds the Pi credentials and settings. Use a provider and model that `pi --list-models` lists for that sign-in. For OpenAI, some models require API-key sign-in even if you have a ChatGPT subscription.

The callback socket identifies the step, not the caller. There is no tool sandbox: processes in the runner can read the Pi credentials and open the shared sockets. The runner does not mount state or the app configuration. Do not mount the shared socket volume in other containers or expose the socket endpoints over TCP.
