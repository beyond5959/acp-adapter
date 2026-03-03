import { readFileSync, writeFileSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const npmRoot = resolve(here, "..");

const version = process.argv[2];
if (!version || !/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version)) {
  console.error("Usage: npm --prefix npm run version:set -- <semver>");
  process.exit(1);
}

const packageFiles = [
  "package.json",
  "packages/acp-adapter/package.json",
  "packages/acp-adapter-darwin-arm64/package.json",
  "packages/acp-adapter-darwin-x64/package.json",
  "packages/acp-adapter-linux-arm64/package.json",
  "packages/acp-adapter-linux-x64/package.json",
  "packages/acp-adapter-win32-arm64/package.json",
  "packages/acp-adapter-win32-x64/package.json",
];

for (const rel of packageFiles) {
  const abs = resolve(npmRoot, rel);
  const doc = JSON.parse(readFileSync(abs, "utf8"));
  doc.version = version;

  if (doc.name === "@beyond5959/acp-adapter" && doc.optionalDependencies) {
    for (const depName of Object.keys(doc.optionalDependencies)) {
      if (depName.startsWith("@beyond5959/acp-adapter-")) {
        doc.optionalDependencies[depName] = version;
      }
    }
  }

  writeFileSync(abs, `${JSON.stringify(doc, null, 2)}\n`);
  console.error(`[version] ${rel} -> ${version}`);
}
