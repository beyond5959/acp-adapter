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
    pkgDir: "npm/packages/acp-adapter-darwin-arm64",
    binaryName: "acp-adapter",
  },
  {
    goos: "darwin",
    goarch: "amd64",
    pkgDir: "npm/packages/acp-adapter-darwin-x64",
    binaryName: "acp-adapter",
  },
  {
    goos: "linux",
    goarch: "arm64",
    pkgDir: "npm/packages/acp-adapter-linux-arm64",
    binaryName: "acp-adapter",
  },
  {
    goos: "linux",
    goarch: "amd64",
    pkgDir: "npm/packages/acp-adapter-linux-x64",
    binaryName: "acp-adapter",
  },
  {
    goos: "windows",
    goarch: "arm64",
    pkgDir: "npm/packages/acp-adapter-win32-arm64",
    binaryName: "acp-adapter.exe",
  },
  {
    goos: "windows",
    goarch: "amd64",
    pkgDir: "npm/packages/acp-adapter-win32-x64",
    binaryName: "acp-adapter.exe",
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
      "./cmd/acp",
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
