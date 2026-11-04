import type * as vscode from "vscode";
import { complete } from "./completion";
import { EditorClient, type EditorDiagnostic, type EditorMethod, type EditorResponse } from "./editor-client";
import { byteColumnToUtf16 } from "./positions";

interface EditorClientLike {
  request(method: EditorMethod, text: string): Promise<EditorResponse>;
  dispose(): void;
}

export interface ActivationDependencies {
  vscode?: typeof vscode;
  createEditorClient?: (extensionPath: string, output: vscode.OutputChannel) => EditorClientLike;
}

let activeEditor: EditorClientLike | undefined;

export function activate(context: vscode.ExtensionContext, dependencies: ActivationDependencies = {}): void {
  const api = dependencies.vscode ?? (require("vscode") as typeof vscode);
  const outputChannel = api.window.createOutputChannel("Dynamox");
  const diagnostics = api.languages.createDiagnosticCollection("dynamox");
  context.subscriptions.push(outputChannel, diagnostics);
  const editor = dependencies.createEditorClient
    ? dependencies.createEditorClient(context.extensionPath, outputChannel)
    : new EditorClient(context.extensionPath, outputChannel);
  activeEditor = editor;

  const timers = new Map<string, ReturnType<typeof setTimeout>>();
  const validateDocument = (document: vscode.TextDocument): void => {
    if (document.languageId !== "dynamox") return;
    const version = document.version;
    editor.request("validate", document.getText()).then((response) => {
      if (!api.workspace.textDocuments.includes(document)) return;
      if (document.version !== version) return;
      diagnostics.set(document.uri, toDiagnostics(api, document, response.diagnostics ?? []));
    }).catch((error: unknown) => outputChannel.appendLine(`Validation failed: ${errorMessage(error)}`));
  };
  const scheduleValidation = (document: vscode.TextDocument): void => {
    const key = document.uri.toString();
    const previous = timers.get(key);
    if (previous !== undefined) clearTimeout(previous);
    const delay = api.workspace.getConfiguration("dynamox").get<number>("validationDelay", 300);
    const timer = setTimeout(() => {
      timers.delete(key);
      validateDocument(document);
    }, delay);
    timers.set(key, timer);
  };

  for (const document of api.workspace.textDocuments) scheduleValidation(document);
  context.subscriptions.push(api.workspace.onDidOpenTextDocument(scheduleValidation));
  context.subscriptions.push(api.workspace.onDidChangeTextDocument((event) => scheduleValidation(event.document)));
  context.subscriptions.push(api.workspace.onDidCloseTextDocument((document) => {
    const key = document.uri.toString();
    const timer = timers.get(key);
    if (timer !== undefined) clearTimeout(timer);
    timers.delete(key);
    diagnostics.delete(document.uri);
  }));

  const formattingProvider: vscode.DocumentFormattingEditProvider = {
    async provideDocumentFormattingEdits(document): Promise<vscode.TextEdit[]> {
      try {
        const response = await editor.request("format", document.getText());
        if (response.diagnostics && response.diagnostics.length > 0) {
          diagnostics.set(document.uri, toDiagnostics(api, document, response.diagnostics));
          return [];
        }
        diagnostics.set(document.uri, []);
        return minimalEdit(api, document, response.formatted ?? "");
      } catch (error: unknown) {
        outputChannel.appendLine(`Formatting failed: ${errorMessage(error)}`);
        return [];
      }
    }
  };
  context.subscriptions.push(api.languages.registerDocumentFormattingEditProvider("dynamox", formattingProvider));

  const kinds: Record<string, vscode.CompletionItemKind> = {
    attribute: api.CompletionItemKind.Function,
    constant: api.CompletionItemKind.Constant,
    field: api.CompletionItemKind.Field,
    keyword: api.CompletionItemKind.Keyword,
    property: api.CompletionItemKind.Property,
    snippet: api.CompletionItemKind.Snippet,
    type: api.CompletionItemKind.Class
  };
  const completionProvider: vscode.CompletionItemProvider = {
    provideCompletionItems(document, position): vscode.CompletionItem[] {
      return complete(document.getText(), document.offsetAt(position)).map((entry) => {
        const item = new api.CompletionItem(entry.label, kinds[entry.kind] ?? api.CompletionItemKind.Text);
        item.detail = entry.detail;
        item.filterText = entry.label;
        item.insertText = entry.kind === "snippet" ? new api.SnippetString(entry.insertText) : entry.insertText;
        return item;
      });
    }
  };
  context.subscriptions.push(api.languages.registerCompletionItemProvider("dynamox", completionProvider, "@", " "));

  context.subscriptions.push({
    dispose(): void {
      for (const timer of timers.values()) clearTimeout(timer);
      timers.clear();
      editor.dispose();
      if (activeEditor === editor) activeEditor = undefined;
    }
  });
}

export function deactivate(): void {
  activeEditor?.dispose();
  activeEditor = undefined;
}

function toDiagnostics(
  api: typeof vscode,
  document: vscode.TextDocument,
  entries: EditorDiagnostic[]
): vscode.Diagnostic[] {
  return entries.map((entry) => {
    const line = Math.max(0, Math.min(document.lineCount - 1, entry.line - 1));
    const lineText = document.lineAt(line).text;
    const start = Math.max(0, Math.min(lineText.length, byteColumnToUtf16(lineText, entry.column)));
    const end = Math.min(lineText.length, start + 1);
    return new api.Diagnostic(
      new api.Range(line, start, line, end),
      entry.message,
      api.DiagnosticSeverity.Error
    );
  });
}

function minimalEdit(api: typeof vscode, document: vscode.TextDocument, formatted: string): vscode.TextEdit[] {
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
  return [api.TextEdit.replace(
    new api.Range(document.positionAt(prefix), document.positionAt(originalEnd)),
    formatted.slice(prefix, formattedEnd)
  )];
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
