"use strict";

const childProcess = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");
const readline = require("node:readline");
const vscode = require("vscode");
const { complete } = require("./completion");
const { byteColumnToUtf16 } = require("./positions");

let editorProcess;
let nextRequestId = 1;
let outputChannel;
const pendingRequests = new Map();

function activate(context) {
  outputChannel = vscode.window.createOutputChannel("Dynamox");
  const diagnostics = vscode.languages.createDiagnosticCollection("dynamox");
  context.subscriptions.push(outputChannel, diagnostics);
  startEditor(context);

  const timers = new Map();
  const validateDocument = (document) => {
    if (document.languageId !== "dynamox") return;
    const version = document.version;
    editorRequest("validate", document.getText()).then((response) => {
      if (vscode.workspace.textDocuments.indexOf(document) < 0) return;
      if (document.version !== version) return;
      diagnostics.set(document.uri, toDiagnostics(document, response.diagnostics || []));
    }).catch((error) => outputChannel.appendLine(`Validation failed: ${error.message}`));
  };
  const scheduleValidation = (document) => {
    const previous = timers.get(document.uri.toString());
    if (previous) clearTimeout(previous);
    const delay = vscode.workspace.getConfiguration("dynamox").get("validationDelay", 300);
    const timer = setTimeout(() => {
      timers.delete(document.uri.toString());
      validateDocument(document);
    }, delay);
    timers.set(document.uri.toString(), timer);
  };

  for (const document of vscode.workspace.textDocuments) scheduleValidation(document);
  context.subscriptions.push(vscode.workspace.onDidOpenTextDocument(scheduleValidation));
  context.subscriptions.push(vscode.workspace.onDidChangeTextDocument((event) => scheduleValidation(event.document)));
  context.subscriptions.push(vscode.workspace.onDidCloseTextDocument((document) => {
    const key = document.uri.toString();
    if (timers.has(key)) clearTimeout(timers.get(key));
    timers.delete(key);
    diagnostics.delete(document.uri);
  }));

  context.subscriptions.push(vscode.languages.registerDocumentFormattingEditProvider("dynamox", {
    async provideDocumentFormattingEdits(document) {
      try {
        const response = await editorRequest("format", document.getText());
        if (response.diagnostics && response.diagnostics.length > 0) {
          diagnostics.set(document.uri, toDiagnostics(document, response.diagnostics));
          return [];
        }
        diagnostics.set(document.uri, []);
        return minimalEdit(document, response.formatted || "");
      } catch (error) {
        outputChannel.appendLine(`Formatting failed: ${error.message}`);
        return [];
      }
    }
  }));

  const kinds = {
    attribute: vscode.CompletionItemKind.Function,
    constant: vscode.CompletionItemKind.Constant,
    field: vscode.CompletionItemKind.Field,
    keyword: vscode.CompletionItemKind.Keyword,
    property: vscode.CompletionItemKind.Property,
    snippet: vscode.CompletionItemKind.Snippet,
    type: vscode.CompletionItemKind.Class
  };
  context.subscriptions.push(vscode.languages.registerCompletionItemProvider("dynamox", {
    provideCompletionItems(document, position) {
      return complete(document.getText(), document.offsetAt(position)).map((entry) => {
        const item = new vscode.CompletionItem(entry.label, kinds[entry.kind] || vscode.CompletionItemKind.Text);
        item.detail = entry.detail;
        item.filterText = entry.label;
        item.insertText = entry.kind === "snippet" ? new vscode.SnippetString(entry.insertText) : entry.insertText;
        return item;
      });
    }
  }, "@", " "));

  context.subscriptions.push({
    dispose() {
      for (const timer of timers.values()) clearTimeout(timer);
      timers.clear();
      stopEditor();
    }
  });
}

function startEditor(context) {
  const goos = process.platform === "win32" ? "windows" : process.platform === "darwin" ? "darwin" : "linux";
  const goarch = process.arch === "x64" ? "amd64" : process.arch;
  const suffix = goos === "windows" ? ".exe" : "";
  const binary = path.join(context.extensionPath, "bin", `dynamox-editor-${goos}-${goarch}${suffix}`);
  if (!fs.existsSync(binary)) {
    outputChannel.appendLine(`Dynamox editor process is missing: ${binary}`);
    return;
  }
  editorProcess = childProcess.spawn(binary, [], { stdio: ["pipe", "pipe", "pipe"] });
  const lines = readline.createInterface({ input: editorProcess.stdout });
  lines.on("line", (line) => {
    let message;
    try {
      message = JSON.parse(line);
    } catch (error) {
      outputChannel.appendLine(`Invalid response from editor process: ${error.message}`);
      return;
    }
    const pending = pendingRequests.get(message.id);
    if (!pending) return;
    pendingRequests.delete(message.id);
    if (message.error) pending.reject(new Error(message.error));
    else pending.resolve(message);
  });
  editorProcess.stderr.on("data", (chunk) => outputChannel.append(chunk.toString()));
  editorProcess.on("error", (error) => rejectPending(error));
  editorProcess.on("exit", (code, signal) => {
    rejectPending(new Error(`Dynamox editor process exited (${signal || code})`));
    editorProcess = undefined;
  });
}

function editorRequest(method, text) {
  if (!editorProcess || !editorProcess.stdin.writable) {
    return Promise.reject(new Error("Dynamox editor process is unavailable"));
  }
  const id = nextRequestId++;
  return new Promise((resolve, reject) => {
    pendingRequests.set(id, { resolve, reject });
    editorProcess.stdin.write(`${JSON.stringify({ id, method, text })}\n`, (error) => {
      if (error) {
        pendingRequests.delete(id);
        reject(error);
      }
    });
  });
}

function rejectPending(error) {
  for (const pending of pendingRequests.values()) pending.reject(error);
  pendingRequests.clear();
}

function stopEditor() {
  if (editorProcess) editorProcess.kill();
  editorProcess = undefined;
  rejectPending(new Error("Dynamox editor process stopped"));
}

function toDiagnostics(document, entries) {
  return entries.map((entry) => {
    const line = Math.max(0, Math.min(document.lineCount - 1, entry.line - 1));
    const lineText = document.lineAt(line).text;
    const start = Math.max(0, Math.min(lineText.length, byteColumnToUtf16(lineText, entry.column)));
    const end = Math.min(lineText.length, start + 1);
    return new vscode.Diagnostic(
      new vscode.Range(line, start, line, end),
      entry.message,
      vscode.DiagnosticSeverity.Error
    );
  });
}

function minimalEdit(document, formatted) {
  const original = document.getText();
  if (original === formatted) return [];
  let prefix = 0;
  while (prefix < original.length && prefix < formatted.length && original[prefix] === formatted[prefix]) prefix += 1;
  let originalEnd = original.length;
  let formattedEnd = formatted.length;
  while (originalEnd > prefix && formattedEnd > prefix && original[originalEnd - 1] === formatted[formattedEnd - 1]) {
    originalEnd -= 1;
    formattedEnd -= 1;
  }
  return [vscode.TextEdit.replace(
    new vscode.Range(document.positionAt(prefix), document.positionAt(originalEnd)),
    formatted.slice(prefix, formattedEnd)
  )];
}

function deactivate() {
  stopEditor();
}

module.exports = { activate, deactivate };
