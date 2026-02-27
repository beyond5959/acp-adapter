import { chmodSync, existsSync, mkdirSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, "..", "..");

const targets = [
  {
    goos: "darwin",
    goarch: "arm64",
    pkgDir: "npm/packages/codex-acp-go-darwin-arm64",
    binaryName: "codex-acp-go",
  },
  {
    goos: "darwin",
    goarch: "amd64",
    pkgDir: "npm/packages/codex-acp-go-darwin-x64",
    binaryName: "codex-acp-go",
  },
  {
    goos: "linux",
    goarch: "arm64",
    pkgDir: "npm/packages/codex-acp-go-linux-arm64",
    binaryName: "codex-acp-go",
  },
  {
    goos: "linux",
    goarch: "amd64",
    pkgDir: "npm/packages/codex-acp-go-linux-x64",
    binaryName: "codex-acp-go",
  },
  {
    goos: "windows",
    goarch: "arm64",
    pkgDir: "npm/packages/codex-acp-go-win32-arm64",
    binaryName: "codex-acp-go.exe",
  },
  {
    goos: "windows",
    goarch: "amd64",
    pkgDir: "npm/packages/codex-acp-go-win32-x64",
    binaryName: "codex-acp-go.exe",
  },
];

const ldflags = process.env.GO_LDFLAGS ?? "-s -w";

for (const target of targets) {
  const outDir = resolve(repoRoot, target.pkgDir, "bin");
  const outPath = resolve(outDir, target.binaryName);

  mkdirSync(outDir, { recursive: true });
  console.error(`[build] ${target.goos}/${target.goarch} -> ${outPath}`);

  const result = spawnSync(
    "go",
    [
      "build",
      "-trimpath",
      "-ldflags",
      ldflags,
      "-o",
      outPath,
      "./cmd/codex-acp-go",
    ],
    {
      cwd: repoRoot,
      stdio: "inherit",
      env: {
        ...process.env,
        CGO_ENABLED: "0",
        GOOS: target.goos,
        GOARCH: target.goarch,
      },
    },
  );

  if (result.status !== 0) {
    process.exit(result.status ?? 1);
  }

  if (!existsSync(outPath)) {
    console.error(`[error] missing build output: ${outPath}`);
    process.exit(1);
  }

  if (target.goos !== "windows") {
    chmodSync(outPath, 0o755);
  }
}

console.error("[build] all target binaries generated");
