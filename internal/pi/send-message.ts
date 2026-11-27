import { request } from "node:http";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";

export default function (pi: ExtensionAPI) {
  const socketPath = process.env.COMPANION_MESSAGE_SOCKET;
  if (!socketPath) throw new Error("Companion message socket is not set");

  pi.registerTool({
    name: "send_message",
    label: "Send message",
    description: "Queue a plain-text message for the user. Use this tool for progress updates and the complete answer.",
    parameters: Type.Object({ text: Type.String() }, { additionalProperties: false }),
    executionMode: "sequential",
    async execute(_id, { text }, signal) {
      if (!text.trim() || Buffer.byteLength(text) > 65536) {
        throw new Error("Send a non-blank message of at most 65536 UTF-8 bytes");
      }
      const body = JSON.stringify({ text });
      await new Promise<void>((resolve, reject) => {
        const failure = () => reject(new Error("Companion did not confirm that it queued the message"));
        const req = request({
          socketPath,
          method: "POST",
          path: "/message",
          headers: { "Content-Type": "application/json", "Content-Length": Buffer.byteLength(body) },
          signal,
        }, (res) => {
          res.on("error", failure);
          res.on("end", () => res.statusCode === 204 ? resolve() : failure());
          res.resume();
        });
        req.on("error", failure);
        req.end(body);
      });
      return { content: [{ type: "text", text: "queued" }], details: {} };
    },
  });
}
