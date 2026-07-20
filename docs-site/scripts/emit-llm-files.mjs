import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const docsSite = path.resolve(__dirname, "..");
const contentRoot = path.join(docsSite, "content");
const dist = path.join(docsSite, "dist");

function stripFrontmatter(raw) {
  if (!raw.startsWith("---")) {
    return { data: {}, body: raw.trim() };
  }
  const end = raw.indexOf("\n---", 3);
  if (end === -1) {
    return { data: {}, body: raw.trim() };
  }
  const block = raw.slice(4, end);
  const body = raw.slice(end + 4).trim();
  const data = {};
  for (const line of block.split("\n")) {
    const idx = line.indexOf(":");
    if (idx === -1) {
      continue;
    }
    const key = line.slice(0, idx).trim();
    let value = line.slice(idx + 1).trim();
    if (
      (value.startsWith('"') && value.endsWith('"')) ||
      (value.startsWith("'") && value.endsWith("'"))
    ) {
      value = value.slice(1, -1);
    }
    data[key] = value;
  }
  return { data, body };
}

function pageIdToSrc(id) {
  if (id === "SPEC") {
    return "SPEC.md";
  }
  return `${id}.md`;
}

function loadPagesFromDocsJson() {
  const docsJsonPath = path.join(contentRoot, "docs.json");
  if (!fs.existsSync(docsJsonPath)) {
    throw new Error(`Missing ${docsJsonPath}; run npm run sync first`);
  }
  const config = JSON.parse(fs.readFileSync(docsJsonPath, "utf8"));
  const ids = config.navigation.groups.flatMap((group) => group.pages);
  return ids.map((id) => {
    const src = pageIdToSrc(id);
    const srcPath = path.join(contentRoot, src);
    if (!fs.existsSync(srcPath)) {
      throw new Error(`docs.json references missing page: ${id} (${src})`);
    }
    const raw = fs.readFileSync(srcPath, "utf8");
    const { data, body } = stripFrontmatter(raw);
    return {
      id,
      title: typeof data.title === "string" ? data.title : id,
      description: typeof data.description === "string" ? data.description : "",
      src,
      mdOut: src,
      raw,
      body,
    };
  });
}

if (!fs.existsSync(dist)) {
  throw new Error(`Missing dist directory at ${dist}; run vite build first`);
}

const pages = loadPagesFromDocsJson();
const docsConfig = JSON.parse(
  fs.readFileSync(path.join(contentRoot, "docs.json"), "utf8"),
);
const siteDescription =
  typeof docsConfig.description === "string"
    ? docsConfig.description
    : "Kubernetes-native control plane for running AI agents as production workloads.";

const llmsLines = [
  "# Kontext",
  "",
  `> ${siteDescription}`,
  "",
  "Full docs: https://docs.kontext.run/llms-full.txt",
  "",
  "## Pages",
  "",
];

const fullParts = [
  "# Kontext Documentation",
  "",
  `> ${siteDescription}`,
  "",
];

for (const page of pages) {
  const outPath = path.join(dist, "raw", page.mdOut);
  fs.mkdirSync(path.dirname(outPath), { recursive: true });
  fs.writeFileSync(outPath, page.raw);

  const desc = page.description ? `: ${page.description}` : "";
  llmsLines.push(
    `- [${page.title}](https://docs.kontext.run/raw/${page.mdOut})${desc}`,
  );
  fullParts.push(`---\n\n# ${page.title}\n\n${page.body}\n`);
}

fs.writeFileSync(path.join(dist, "llms.txt"), `${llmsLines.join("\n")}\n`);
fs.writeFileSync(path.join(dist, "llms-full.txt"), `${fullParts.join("\n")}\n`);

console.log(
  `Wrote llms.txt, llms-full.txt, and ${pages.length} markdown mirrors into dist/`,
);
