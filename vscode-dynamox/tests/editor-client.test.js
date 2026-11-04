"use strict";

const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");
const { PassThrough } = require("node:stream");
const test = require("node:test");
const { EditorClient } = require("../out/src/editor-client");

class FakeProcess extends EventEmitter {
  stdin = new PassThrough();
  stdout = new PassThrough();
  stderr = new PassThrough();
  kill() {
    this.emit("exit", null, "SIGTERM");
    return true;
  }
}

function startClient({ exists = true, platform = process.platform, architecture = process.arch } = {}) {
  const output = { lines: [], text: "", appendLine(value) { this.lines.push(value); }, append(value) { this.text += value; } };
  const child = new FakeProcess();
  const calls = [];
  const client = new EditorClient("/extension", output, {
    platform,
    architecture,
    existsSync(binary) { calls.push(binary); return exists; },
    spawn(...args) { calls.push(args); return child; }
  });
  return { client, child, output, calls };
}

test("reports a missing editor executable and rejects requests", async () => {
  const app = startClient({ exists: false, platform: "win32", architecture: "arm64" });
  assert.match(app.calls[0], /dynamox-editor-windows-arm64\.exe$/);
  assert.ok(app.output.lines[0].includes("process is missing"));
  await assert.rejects(app.client.request("validate", "model X {}"), /process is unavailable/);
  app.client.dispose();
});

test("sends numbered requests, resolves responses, and forwards stderr", async () => {
  const app = startClient({ platform: "darwin", architecture: "x64" });
  assert.match(app.calls[0], /dynamox-editor-darwin-amd64$/);
  app.child.stdin.on("data", (chunk) => {
    const request = JSON.parse(String(chunk));
    app.child.stdout.write(`${JSON.stringify({ id: request.id, formatted: request.text.toUpperCase() })}\n`);
  });
  app.child.stderr.write("engine log");
  const result = await app.client.request("format", "model User {}");
  assert.equal(result.formatted, "MODEL USER {}");
  assert.equal(app.output.text, "engine log");
  app.client.dispose();
});

test("logs malformed responses, ignores unknown ids, and rejects engine errors", async () => {
  const app = startClient();
  const pending = app.client.request("validate", "model User {}");
  app.child.stdout.write("not json\n");
  app.child.stdout.write('{"id":"wrong type"}\n');
  app.child.stdout.write('{"id":999,"diagnostics":[]}\n');
  app.child.stdout.write('{"id":1,"error":"schema parse failed"}\n');
  await assert.rejects(pending, /schema parse failed/);
  assert.ok(app.output.lines.some((line) => line.includes("Unexpected token") || line.includes("not valid JSON")));
  assert.ok(app.output.lines.some((line) => line.includes("missing numeric request id")));
  app.client.dispose();
});

test("write errors reject their request and process errors reject all pending requests", async () => {
  const app = startClient();
  app.child.stdin.write = (_chunk, callback) => {
    callback(new Error("broken pipe"));
    return true;
  };
  await assert.rejects(app.client.request("format", ""), /broken pipe/);

  const second = startClient();
  const pending = second.client.request("validate", "model X {}");
  second.child.emit("error", new Error("spawn failure"));
  await assert.rejects(pending, /spawn failure/);
  await assert.rejects(second.client.request("validate", ""), /process is unavailable/);
  second.client.dispose();
});

test("disposing an editor client rejects pending work and kills the process", async () => {
  const app = startClient();
  const pending = app.client.request("validate", "model X {}");
  app.client.dispose();
  await assert.rejects(pending, /Dynamox editor process exited/);
  await assert.rejects(app.client.request("validate", ""), /process is unavailable/);
});
