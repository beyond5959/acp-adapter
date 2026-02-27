import { mkdirSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const here = dirname(fileURLToPath(import.meta.url));
const npmRoot = resolve(here, "..");
const npmCache = resolve(npmRoot, ".npm-cache");
mkdirSync(npmCache, { recursive: true });

const result = spawnSync("npm", ["--workspaces", "pack"], {
  cwd: npmRoot,
  stdio: "inherit",
  env: {
    ...process.env,
    NPM_CONFIG_CACHE: npmCache,
  },
});

if (result.status !== 0) {
  process.exit(result.status ?? 1);
}
