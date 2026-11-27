const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const http = require('node:http');
const { setTimeout: sleep } = require('node:timers/promises');
const { zstdDecompressSync } = require('node:zlib');

const phase = process.env.PROBE_PHASE;
const model = 'gpt-6-luna';
const cwd = '/workspace';
const profile = 'companion-workspace';
const marker = 'runner-probe-marker';
const reactionTool = 'telegram_react';
const reactionCall = 'probe-reaction';
const reactionAck = 'reaction queued';
const signIn = '/home/node/.codex/auth.json';
assert(['create', 'verify'].includes(phase));
fs.writeFileSync(signIn, JSON.stringify({auth_mode: 'apikey', OPENAI_API_KEY: 'disposable-probe-key'}), {mode: 0o600});

const checks = [
  'set -eu',
  'test "$PWD" = /workspace',
  'test "$HOME" = /workspace',
  'test -z "${OPENAI_API_KEY:-}"',
  `test ! -r ${signIn}`,
  `for root in /proc/[0-9]*/root; do test ! -r "$root${signIn}"; done`,
  'test ! -e /app/state && test ! -e /etc/companion/companion.json || exit 1',
  'if (echo fail > /tmp/workspace-probe); then exit 1; fi',
  'node -e \'fetch("http://probe-http:3900", {signal: AbortSignal.timeout(2000)}).then(async res => require("node:assert/strict").equal(await res.text(), "probe network"))\'',
  ...(phase === 'create' ? [
    'mkdir probe-files',
    'printf original > probe-files/draft',
    'mv probe-files/draft probe-files/saved',
    'printf durable > probe-files/saved',
    'printf temporary > probe-files/remove',
    'rm probe-files/remove',
  ] : [
    'test "$(cat probe-files/saved)" = durable',
    'test ! -e probe-files/draft && test ! -e probe-files/remove || exit 1',
    'rm probe-files/saved; rmdir probe-files',
  ]),
  "printf '%s%s' runner-probe- marker",
].join('\n');

let responses = 0;
const api = http.createServer(async (req, res) => {
  if (req.method !== 'POST' || !req.url.endsWith('/responses')) {
    res.writeHead(req.url.endsWith('/responses') ? 426 : 404).end();
    return;
  }
  const chunks = [];
  for await (const chunk of req) chunks.push(chunk);
  let body = Buffer.concat(chunks);
  if (req.headers['content-encoding'] === 'zstd') body = zstdDecompressSync(body);
  const request = JSON.parse(body);
  assert.equal(request.model, model);
  let item;
  const id = `response-${++responses}`;
  if (responses === 1) {
    assert(JSON.stringify(request).includes(reactionTool), 'Reaction tool missing from model request');
    const script = `if (typeof tools.exec_command === 'function') text(await tools.exec_command({cmd:${JSON.stringify(checks)},workdir:${JSON.stringify(cwd)},yield_time_ms:3000})); else text(await tools.shell_command({command:${JSON.stringify(checks)},workdir:${JSON.stringify(cwd)},timeout_ms:3000}));`;
    item = {type: 'custom_tool_call', namespace: 'functions', name: 'exec', call_id: 'probe-call', input: script};
  } else if (responses === 2) {
    const output = JSON.stringify(request.input.filter(i => i.call_id === 'probe-call' && i.type === 'custom_tool_call_output'));
    assert(output.includes(marker), `Sandbox check failed: ${output}`);
    item = {type: 'custom_tool_call', namespace: 'functions', name: 'exec', call_id: reactionCall,
      input: `text(await tools.${reactionTool}({emoji: "👍"}));`};
  } else {
    assert.equal(responses, 3);
    const output = JSON.stringify(request.input.filter(i => i.call_id === reactionCall && i.type === 'custom_tool_call_output'));
    assert(output.includes(reactionAck), `Reaction tool check failed: ${output}`);
    item = {id: 'probe-result', type: 'message', role: 'assistant', phase: 'final_answer', content: [{type: 'output_text', text: 'workspace checked'}]};
  }
  const events = [
    {type: 'response.created', response: {id}},
    {type: 'response.output_item.done', item},
    {type: 'response.completed', response: {id, usage: {input_tokens: 0, input_tokens_details: null, output_tokens: 0, output_tokens_details: null, total_tokens: 0}}},
  ];
  res.writeHead(200, {'content-type': 'text/event-stream'}).end(events.map(event => `data: ${JSON.stringify(event)}\n\n`).join(''));
});

(async () => {
  await new Promise(resolve => api.listen(3900, '127.0.0.1', resolve));
  const runner = spawn('codex', ['app-server', ...process.argv.slice(2), '-c', 'openai_base_url="http://127.0.0.1:3900/v1"'], {
    env: {...process.env, OPENAI_API_KEY: 'disposable-probe-key'}, stdio: 'inherit',
  });
  let ws;
  const timeout = setTimeout(() => { throw Error('Runner probe timed out'); }, 30000);
  try {
    for (;;) {
      assert.equal(runner.exitCode, null, 'app-server exited before it was ready');
      if (await fetch('http://127.0.0.1:4500/readyz').then(r => r.ok, () => false)) break;
      await sleep(50);
    }
    ws = new WebSocket('ws://127.0.0.1:4500');
    await new Promise((resolve, reject) => {
      ws.addEventListener('open', resolve, {once: true});
      ws.addEventListener('error', reject, {once: true});
    });
    const pending = new Map();
    let next = 0;
    let complete;
    let threadID;
    const done = new Promise(resolve => { complete = resolve; });
    ws.onmessage = ({data}) => {
      const message = JSON.parse(data);
      if (message.id != null && message.method) {
        assert.equal(message.method, 'item/tool/call');
        const call = message.params;
        assert.equal(call.threadId, threadID);
        assert.equal(call.tool, reactionTool);
        assert(typeof call.callId === 'string' && call.callId.length > 0);
        assert.equal(call.namespace, null);
        assert.deepEqual(call.arguments, {emoji: '👍'});
        ws.send(JSON.stringify({id: message.id, result: {success: true, contentItems: [{type: 'inputText', text: reactionAck}]}}));
        return;
      }
      if (message.id != null) pending.get(message.id)?.(message);
      if (message.method === 'turn/completed') complete(message.params.turn.status);
    };
    const rpc = async (method, params) => {
      const id = ++next;
      const result = new Promise(resolve => pending.set(id, resolve));
      ws.send(JSON.stringify({id, method, params}));
      const reply = await result;
      pending.delete(id);
      assert(!reply.error, `${method}: ${JSON.stringify(reply.error)}`);
      return reply.result;
    };
    await rpc('initialize', {clientInfo: {name: 'workspace-probe', version: '1'}, capabilities: {experimentalApi: true}});
    ws.send(JSON.stringify({method: 'initialized'}));
    const restrictions = {model, cwd, runtimeWorkspaceRoots: [cwd], permissions: profile, approvalPolicy: 'never', approvalsReviewer: 'user'};
    const thread = await rpc('thread/start', {...restrictions, ephemeral: true, modelProvider: 'openai', allowProviderModelFallback: false,
      config: {web_search: 'live', features: {standalone_web_search: true}},
      dynamicTools: [{type: 'function', name: reactionTool, description: 'React to a Telegram message.',
        inputSchema: {type: 'object', properties: {emoji: {type: 'string', enum: ['👍']}}, required: ['emoji'], additionalProperties: false}}]});
    threadID = thread.thread.id;
    assert.equal(thread.activePermissionProfile.id, profile);
    assert.equal(thread.sandbox.type, 'workspaceWrite');
    assert.equal(thread.sandbox.networkAccess, true);
    assert.equal(thread.sandbox.excludeSlashTmp, true);
    assert.equal(thread.sandbox.excludeTmpdirEnvVar, true);
    await rpc('turn/start', {...restrictions, threadId: thread.thread.id, input: [{type: 'text', text: 'Run the workspace probe.', text_elements: []}]});
    assert.equal(await done, 'completed');
    assert.equal(responses, 3);
    if (phase === 'create') {
      assert.equal(fs.readFileSync('probe-files/saved', 'utf8'), 'durable');
      assert.deepEqual(fs.readdirSync('probe-files'), ['saved']);
    } else {
      assert(!fs.existsSync('probe-files'));
    }
    console.log(`Runner workspace probe passed: ${phase}.`);
  } finally {
    ws?.close();
    runner.kill();
    api.closeAllConnections();
    api.close();
    clearTimeout(timeout);
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
