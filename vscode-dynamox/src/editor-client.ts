import * as childProcess from "node:child_process";
import * as fs from "node:fs";
import * as path from "node:path";
import * as readline from "node:readline";
import type { OutputChannel } from "vscode";

export interface EditorDiagnostic {
  line: number;
  column: number;
  message: string;
}

export interface EditorResponse {
  id: number;
  diagnostics?: EditorDiagnostic[];
  formatted?: string;
  error?: string;
}

export type EditorMethod = "validate" | "format";
const maxSchemaBytes = 1024 * 1024;

interface PendingRequest {
  resolve: (response: EditorResponse) => void;
  reject: (error: Error) => void;
}

export interface EditorClientDependencies {
  platform?: NodeJS.Platform;
  architecture?: string;
  lstatSync?: typeof fs.lstatSync;
  spawn?: typeof childProcess.spawn;
}

export class EditorClient {
  private editorProcess: childProcess.ChildProcessWithoutNullStreams | undefined;
  private nextRequestId = 1;
  private readonly pendingRequests = new Map<number, PendingRequest>();

  constructor(
    extensionPath: string,
    private readonly outputChannel: OutputChannel,
    dependencies: EditorClientDependencies = {}
  ) {
    const platform = dependencies.platform ?? process.platform;
    const architecture = dependencies.architecture ?? process.arch;
    const goosByPlatform: Partial<Record<NodeJS.Platform, string>> = { darwin: "darwin", linux: "linux", win32: "windows" };
    const goarchByArchitecture: Record<string, string | undefined> = { x64: "amd64", arm64: "arm64" };
    const goos = goosByPlatform[platform];
    const goarch = goarchByArchitecture[architecture];
    if (!goos || !goarch) {
      outputChannel.appendLine(`Dynamox editor process is unavailable for ${platform}/${architecture}`);
      return;
    }
    const suffix = goos === "windows" ? ".exe" : "";
    const binDirectory = path.join(extensionPath, "bin");
    const binary = path.join(binDirectory, `dynamox-editor-${goos}-${goarch}${suffix}`);
    const lstatSync = dependencies.lstatSync ?? fs.lstatSync;
    let binStat: fs.Stats;
    let binaryStat: fs.Stats;
    try {
      binStat = lstatSync(binDirectory);
      binaryStat = lstatSync(binary);
    } catch {
      outputChannel.appendLine(`Dynamox editor process is missing: ${binary}`);
      return;
    }
    if (!binStat.isDirectory() || binStat.isSymbolicLink() || !binaryStat.isFile() || binaryStat.isSymbolicLink()) {
      outputChannel.appendLine("Dynamox editor process path is not a regular bundled file.");
      return;
    }

    const spawn = dependencies.spawn ?? childProcess.spawn;
    const editorProcess = spawn(binary, [], { stdio: ["pipe", "pipe", "pipe"] });
    this.editorProcess = editorProcess;
    const lines = readline.createInterface({ input: editorProcess.stdout });
    lines.on("line", (line: string) => this.handleLine(line));
    editorProcess.stderr.on("data", (chunk: Buffer | string) => outputChannel.append(chunk.toString()));
    editorProcess.on("error", (error: Error) => {
      if (this.editorProcess === editorProcess) this.editorProcess = undefined;
      this.rejectPending(error);
    });
    editorProcess.on("exit", (code, signal) => {
      if (this.editorProcess === editorProcess) this.editorProcess = undefined;
      this.rejectPending(new Error(`Dynamox editor process exited (${signal || code})`));
    });
  }

  request(method: EditorMethod, text: string): Promise<EditorResponse> {
    if (Buffer.byteLength(text, "utf8") > maxSchemaBytes) {
      return Promise.reject(new Error(`Dynamox schema exceeds the ${maxSchemaBytes}-byte editor limit`));
    }
    const editorProcess = this.editorProcess;
    if (!editorProcess || !editorProcess.stdin.writable) {
      return Promise.reject(new Error("Dynamox editor process is unavailable"));
    }

    const id = this.nextRequestId++;
    const line = `${JSON.stringify({ id, method, text })}\n`;
    return new Promise((resolve, reject) => {
      this.pendingRequests.set(id, { resolve, reject });
      editorProcess.stdin.write(line, (error?: Error | null) => {
        if (!error) return;
        this.pendingRequests.delete(id);
        reject(error);
      });
    });
  }

  dispose(): void {
    if (this.editorProcess) this.editorProcess.kill();
    this.editorProcess = undefined;
    this.rejectPending(new Error("Dynamox editor process stopped"));
  }

  private handleLine(line: string): void {
    let message: unknown;
    try {
      message = JSON.parse(line);
    } catch (error) {
      this.outputChannel.appendLine(`Invalid response from editor process: ${errorMessage(error)}`);
      return;
    }
    if (!isEditorResponse(message)) {
      this.outputChannel.appendLine("Invalid response from editor process: missing numeric request id");
      return;
    }

    const pending = this.pendingRequests.get(message.id);
    if (!pending) return;
    this.pendingRequests.delete(message.id);
    if (message.error) pending.reject(new Error(message.error));
    else pending.resolve(message);
  }

  private rejectPending(error: Error): void {
    for (const pending of this.pendingRequests.values()) pending.reject(error);
    this.pendingRequests.clear();
  }
}

function isEditorResponse(value: unknown): value is EditorResponse {
  return typeof value === "object" && value !== null && "id" in value && typeof value.id === "number";
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
