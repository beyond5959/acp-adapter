import { mkdirSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const here = dirname(fileURLToPath(import.meta.url));
const npmRoot = resolve(here, "..");
const npmCache = resolve(npmRoot, ".npm-cache");
mkdirSync(npmCache, { recursive: true });

const packages = [
  "@beyond5959/codex-acp-go-darwin-arm64",
  "@beyond5959/codex-acp-go-darwin-x64",
  "@beyond5959/codex-acp-go-linux-arm64",
  "@beyond5959/codex-acp-go-linux-x64",
  "@beyond5959/codex-acp-go-win32-arm64",
  "@beyond5959/codex-acp-go-win32-x64",
  "@beyond5959/codex-acp-go",
];

for (const pkg of packages) {
  console.error(`[publish] ${pkg}`);
  const result = spawnSync(
    "npm",
    ["publish", "--workspace", pkg, "--access", "public"],
    {
      cwd: npmRoot,
      stdio: "inherit",
      env: {
        ...process.env,
        NPM_CONFIG_CACHE: npmCache,
      },
    },
  );
  if (result.status !== 0) {
    process.exit(result.status ?? 1);
  }
}

console.error("[publish] completed");
