import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const docsSite = path.resolve(__dirname, "..");
const repoRoot = path.resolve(docsSite, "..");
const contentRoot = path.join(docsSite, "content");

const sources = [
  { from: "docs-nav.json", to: "docs-nav.json" },
  { from: "SPEC.md", to: "SPEC.md" },
];

function copyFile(fromRel, toRel) {
  const from = path.join(repoRoot, fromRel);
  const to = path.join(contentRoot, toRel);
  fs.mkdirSync(path.dirname(to), { recursive: true });
  fs.copyFileSync(from, to);
}

function copyDir(fromRel, toRel) {
  const from = path.join(repoRoot, fromRel);
  const to = path.join(contentRoot, toRel);
  fs.mkdirSync(to, { recursive: true });
  for (const entry of fs.readdirSync(from, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      copyDir(path.join(fromRel, entry.name), path.join(toRel, entry.name));
      continue;
    }
    if (!entry.name.endsWith(".md")) {
      continue;
    }
    fs.copyFileSync(
      path.join(from, entry.name),
      path.join(to, entry.name),
    );
  }
}

const docsDir = path.join(repoRoot, "docs");
if (!fs.existsSync(docsDir)) {
  if (!fs.existsSync(path.join(contentRoot, "docs"))) {
    throw new Error(
      "Missing repository docs/ and docs-site/content/. Run sync from the repo checkout.",
    );
  }
  console.log("Using vendored docs-site/content/ (no repo docs/ in this environment)");
  process.exit(0);
}

fs.rmSync(contentRoot, { recursive: true, force: true });
fs.mkdirSync(contentRoot, { recursive: true });

for (const item of sources) {
  copyFile(item.from, item.to);
}
copyDir("docs", "docs");

console.log(`Synced docs content into ${path.relative(repoRoot, contentRoot)}`);
