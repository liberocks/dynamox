"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const { activate, deactivate } = require("../out/src/extension");

class Disposable {
  constructor(onDispose = () => {}) {
    this.disposed = false;
    this.onDispose = onDispose;
  }
  dispose() {
    this.disposed = true;
    this.onDispose();
  }
}

class OutputChannel extends Disposable {
  lines = [];
  text = "";
  appendLine(value) { this.lines.push(value); }
  append(value) { this.text += value; }
}

class DiagnosticCollection extends Disposable {
  values = new Map();
  deleted = [];
  set(uri, values) { this.values.set(uri.toString(), values); }
  delete(uri) { this.deleted.push(uri.toString()); this.values.delete(uri.toString()); }
}

class Range {
  constructor(startLine, startCharacter, endLine, endCharacter) {
    if (typeof startLine === "object") {
      this.start = startLine;
      this.end = startCharacter;
    } else {
      this.start = { line: startLine, character: startCharacter };
      this.end = { line: endLine, character: endCharacter };
    }
  }
}

class Diagnostic {
  constructor(range, message, severity) {
    this.range = range;
    this.message = message;
    this.severity = severity;
  }
}

class CompletionItem {
  constructor(label, kind) { this.label = label; this.kind = kind; }
}

class SnippetString {
  constructor(value) { this.value = value; }
}

function makeDocument(text, uri = "file:///sample.dynamox", languageId = "dynamox") {
  let records = text;
  const lines = () => records.split("\n");
  return {
    languageId,
    version: 1,
    uri: { toString: () => uri },
    get lineCount() { return lines().length; },
    getText: () => records,
    setText(value) { records = value; this.version += 1; },
    lineAt: (line) => ({ text: lines()[line] ?? "" }),
    offsetAt(position) {
      const before = lines().slice(0, position.line).join("\n");
      return (before.length ? before.length + 1 : 0) + position.character;
    },
    positionAt(offset) {
      const safeOffset = Math.max(0, Math.min(records.length, offset));
      const before = records.slice(0, safeOffset);
      const split = before.split("\n");
      return { line: split.length - 1, character: split[split.length - 1].length };
    }
  };
}

function harness({ documents = [], validationDelay = 0, request } = {}) {
  const events = {};
  const registrations = {};
  const output = new OutputChannel();
  const diagnostics = new DiagnosticCollection();
  const requests = [];
  const client = {
    disposed: 0,
    request(method, text) {
      requests.push({ method, text });
      return request ? request(method, text) : Promise.resolve({ id: requests.length, diagnostics: [], formatted: text });
    },
    dispose() { this.disposed += 1; }
  };
  const disposable = () => new Disposable();
  const api = {
    window: { createOutputChannel: () => output },
    workspace: {
      textDocuments: documents,
      getConfiguration: () => ({ get: (_name, fallback) => validationDelay ?? fallback }),
      onDidOpenTextDocument: (callback) => { events.open = callback; return disposable(); },
      onDidChangeTextDocument: (callback) => { events.change = callback; return disposable(); },
      onDidCloseTextDocument: (callback) => { events.close = callback; return disposable(); }
    },
    languages: {
      createDiagnosticCollection: () => diagnostics,
      registerDocumentFormattingEditProvider: (_selector, provider) => { registrations.format = provider; return disposable(); },
      registerCompletionItemProvider: (_selector, provider, ...triggers) => { registrations.completion = provider; registrations.triggers = triggers; return disposable(); }
    },
    CompletionItemKind: { Function: 1, Constant: 2, Field: 3, Keyword: 4, Property: 5, Snippet: 6, Class: 7, Text: 8 },
    CompletionItem,
    SnippetString,
    Diagnostic,
    DiagnosticSeverity: { Error: 1 },
    Range,
    TextEdit: { replace: (range, newText) => ({ range, newText }) }
  };
  const context = { extensionPath: "/extension", subscriptions: [] };
  activate(context, { vscode: api, createEditorClient: () => client });
  return { api, context, events, registrations, output, diagnostics, requests, client };
}

const tick = (ms = 10) => new Promise((resolve) => setTimeout(resolve, ms));

test("activation registers language features and debounces validation", async () => {
  const dynamox = makeDocument("model User {\n id String @id\n}");
  const plain = makeDocument("text", "file:///plain.txt", "plaintext");
  const app = harness({ documents: [dynamox, plain], request: async (method, text) => ({
    id: 1,
    diagnostics: method === "validate" ? [{ line: 99, column: 99, message: text }] : [],
    formatted: text
  }) });
  await tick();
  assert.deepEqual(app.requests.map((entry) => entry.method), ["validate"]);
  assert.deepEqual(app.registrations.triggers, ["@", " "]);
  const found = app.diagnostics.values.get(dynamox.uri.toString());
  assert.equal(found[0].range.start.line, dynamox.lineCount - 1);
  assert.equal(found[0].range.start.character, dynamox.lineAt(dynamox.lineCount - 1).text.length);

  const newDoc = makeDocument("model Post {", "file:///post.dynamox");
  app.api.workspace.textDocuments.push(newDoc);
  app.events.open(newDoc);
  app.events.change({ document: newDoc });
  await tick();
  assert.equal(app.requests.filter((entry) => entry.text === newDoc.getText()).length, 1);
});

test("validation ignores stale or closed documents and logs editor failures", async () => {
  let resolveRequest;
  const doc = makeDocument("model Stale {", "file:///stale.dynamox");
  const app = harness({ documents: [], request: () => new Promise((resolve) => { resolveRequest = resolve; }) });
  app.api.workspace.textDocuments.push(doc);
  app.events.open(doc);
  await tick();
  doc.setText("model Changed {");
  resolveRequest({ id: 1, diagnostics: [{ line: 1, column: 1, message: "stale" }] });
  await tick(0);
  assert.equal(app.diagnostics.values.has(doc.uri.toString()), false);

  app.events.close(doc);
  assert.deepEqual(app.diagnostics.deleted, [doc.uri.toString()]);

  const failed = harness({ documents: [], request: async () => { throw new Error("engine unavailable"); } });
  const failingDoc = makeDocument("model Broken {", "file:///broken.dynamox");
  failed.api.workspace.textDocuments.push(failingDoc);
  failed.events.open(failingDoc);
  await tick();
  assert.ok(failed.output.lines.some((line) => line.includes("Validation failed: engine unavailable")));
});

test("closing and disposing the extension clears pending validation timers", async () => {
  const app = harness({ documents: [], validationDelay: 50 });
  const doc = makeDocument("model Pending {", "file:///pending.dynamox");
  app.api.workspace.textDocuments.push(doc);
  app.events.open(doc);
  app.events.change({ document: doc });
  app.events.close(doc);
  assert.equal(app.requests.length, 0);

  const cleanup = app.context.subscriptions.at(-1);
  cleanup.dispose();
  assert.equal(app.client.disposed, 1);
  deactivate();
  assert.equal(app.client.disposed, 1);
});

test("formatting returns minimal edits, clears diagnostics, and reports invalid schemas", async () => {
  const responses = [
    { id: 1, formatted: "abc\n" },
    { id: 2, formatted: "abc\n" },
    { id: 3, diagnostics: [{ line: 1, column: 2, message: "bad schema" }] }
  ];
  const app = harness({ request: async () => responses.shift() });
  const doc = makeDocument("axc\n");
  const edit = await app.registrations.format.provideDocumentFormattingEdits(doc);
  assert.equal(edit.length, 1);
  assert.equal(edit[0].newText, "b");
  assert.equal(edit[0].range.start.character, 1);
  assert.deepEqual(await app.registrations.format.provideDocumentFormattingEdits(makeDocument("abc\n")), []);
  assert.deepEqual(await app.registrations.format.provideDocumentFormattingEdits(doc), []);
  assert.equal(app.diagnostics.values.get(doc.uri.toString())[0].message, "bad schema");
});

test("formatting logs failures and completions map kinds and snippet text", async () => {
  const app = harness({ request: async (method, text) => {
    if (method === "format") throw new Error("format failed");
    return { id: 1, diagnostics: [], formatted: text };
  } });
  assert.deepEqual(await app.registrations.format.provideDocumentFormattingEdits(makeDocument("bad")), []);
  assert.ok(app.output.lines.some((line) => line.includes("Formatting failed: format failed")));
  const text = "";
  const items = app.registrations.completion.provideCompletionItems(makeDocument(text), { line: 0, character: 0 });
  assert.equal(items[0].kind, app.api.CompletionItemKind.Snippet);
  assert.ok(items[0].insertText instanceof SnippetString);
});
