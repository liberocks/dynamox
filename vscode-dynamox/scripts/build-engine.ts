import { spawnSync } from "node:child_process";
import * as fs from "node:fs";
import * as path from "node:path";

const goosByNode: Record<string, string> = { darwin: "darwin", linux: "linux", win32: "windows" };
const goarchByNode: Record<string, string> = { x64: "amd64", arm64: "arm64" };
const goos = process.env.GOOS || goosByNode[process.platform];
const goarch = process.env.GOARCH || goarchByNode[process.arch];
const supportedTargets = new Set([
  "darwin/amd64",
  "darwin/arm64",
  "linux/amd64",
  "linux/arm64",
  "windows/amd64",
  "windows/arm64"
]);
if (!goos || !goarch || !supportedTargets.has(`${goos}/${goarch}`)) {
  process.stderr.write(`Unsupported build target: ${process.platform}/${process.arch}\n`);
  process.exit(1);
}

const extensionRoot = path.resolve(__dirname, "../..");
const binaryName = `dynamox-editor-${goos}-${goarch}${goos === "windows" ? ".exe" : ""}`;
const binDirectory = path.join(extensionRoot, "bin");
fs.mkdirSync(binDirectory, { recursive: true });
const binStat = lstatIfPresent(binDirectory);
if (!binStat?.isDirectory() || binStat.isSymbolicLink()) {
  process.stderr.write("Refusing to build through a symbolic-link bin directory.\n");
  process.exit(1);
}
const output = path.join(binDirectory, binaryName);
const outputStat = lstatIfPresent(output);
if (outputStat && (!outputStat.isFile() || outputStat.isSymbolicLink() || outputStat.nlink > 1)) {
  process.stderr.write("Refusing to overwrite a non-file editor binary path.\n");
  process.exit(1);
}
const buildDirectory = fs.mkdtempSync(path.join(binDirectory, ".dynamox-build-"));
const temporaryOutput = path.join(buildDirectory, binaryName);
const result = spawnSync("go", ["build", "-trimpath", "-o", temporaryOutput, "../cmd/dynamox-editor"], {
  cwd: extensionRoot,
  stdio: "inherit",
  env: { ...process.env, CGO_ENABLED: "0", GOOS: goos, GOARCH: goarch }
});
if (result.error) {
  fs.rmSync(buildDirectory, { recursive: true, force: true });
  process.stderr.write(`${result.error.message}\n`);
  process.exit(1);
}
if (result.status !== 0) {
  fs.rmSync(buildDirectory, { recursive: true, force: true });
  process.exit(result.status ?? 1);
}
const currentOutput = lstatIfPresent(output);
if (currentOutput && (!currentOutput.isFile() || currentOutput.isSymbolicLink() || currentOutput.nlink > 1)) {
  fs.rmSync(buildDirectory, { recursive: true, force: true });
  process.stderr.write("Refusing to replace a non-regular editor binary path.\n");
  process.exit(1);
}
if (currentOutput) fs.unlinkSync(output);
fs.renameSync(temporaryOutput, output);
fs.rmSync(buildDirectory, { recursive: true, force: true });
process.stdout.write(`Built ${path.relative(extensionRoot, output)}\n`);

function lstatIfPresent(target: string): fs.Stats | undefined {
  try {
    return fs.lstatSync(target);
  } catch (error) {
    if (typeof error === "object" && error !== null && "code" in error && error.code === "ENOENT") return undefined;
    throw error;
  }
}
