"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { spawnSync } = require("node:child_process");

const goosByNode = { darwin: "darwin", linux: "linux", win32: "windows" };
const goarchByNode = { x64: "amd64", arm64: "arm64" };
const goos = process.env.GOOS || goosByNode[process.platform];
const goarch = process.env.GOARCH || goarchByNode[process.arch];
if (!goos || !goarch) {
  process.stderr.write(`Unsupported build target: ${process.platform}/${process.arch}\n`);
  process.exit(1);
}

const extensionRoot = path.resolve(__dirname, "..");
const binaryName = `dynamox-editor-${goos}-${goarch}${goos === "windows" ? ".exe" : ""}`;
const output = path.join(extensionRoot, "bin", binaryName);
fs.mkdirSync(path.dirname(output), { recursive: true });
const result = spawnSync("go", ["build", "-trimpath", "-o", output, "../cmd/dynamox-editor"], {
  cwd: extensionRoot,
  stdio: "inherit",
  env: { ...process.env, CGO_ENABLED: "0", GOOS: goos, GOARCH: goarch }
});
if (result.error) {
  process.stderr.write(`${result.error.message}\n`);
  process.exit(1);
}
if (result.status !== 0) process.exit(result.status || 1);
process.stdout.write(`Built ${path.relative(extensionRoot, output)}\n`);
