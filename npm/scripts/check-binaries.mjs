import { statSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, "..", "..");

const expected = [
  "npm/packages/codex-acp-go-darwin-arm64/bin/codex-acp-go",
  "npm/packages/codex-acp-go-darwin-x64/bin/codex-acp-go",
  "npm/packages/codex-acp-go-linux-arm64/bin/codex-acp-go",
  "npm/packages/codex-acp-go-linux-x64/bin/codex-acp-go",
  "npm/packages/codex-acp-go-win32-arm64/bin/codex-acp-go.exe",
  "npm/packages/codex-acp-go-win32-x64/bin/codex-acp-go.exe",
];

for (const rel of expected) {
  const abs = resolve(repoRoot, rel);
  let stat;
  try {
    stat = statSync(abs);
  } catch (err) {
    console.error(`[error] binary not found: ${rel}`);
    process.exit(1);
  }

  if (!stat.isFile() || stat.size <= 0) {
    console.error(`[error] invalid binary file: ${rel}`);
    process.exit(1);
  }

  console.error(`[ok] ${rel} (${stat.size} bytes)`);
}

console.error("[check] all expected binaries are present");
