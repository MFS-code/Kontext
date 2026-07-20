import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { parseFrontmatter, requirePageMetadata } from "../shared/docs.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const docsSite = path.resolve(__dirname, "..");
const contentRoot = path.join(docsSite, "content");
const dist = path.join(docsSite, "dist");

function loadPagesFromNav() {
  const navPath = path.join(contentRoot, "docs-nav.json");
  if (!fs.existsSync(navPath)) {
    throw new Error(`Missing ${navPath}; run npm run sync first`);
  }
  const config = JSON.parse(fs.readFileSync(navPath, "utf8"));
  const ids = config.navigation.groups.flatMap((group) => group.pages);
  return {
    config,
    pages: ids.map((id) => {
      const metadata = requirePageMetadata(id);
      const srcPath = path.join(contentRoot, metadata.srcFile);
      if (!fs.existsSync(srcPath)) {
        throw new Error(
          `docs-nav.json references missing page: ${id} (${metadata.srcFile})`,
        );
      }
      const raw = fs.readFileSync(srcPath, "utf8");
      const { data, content } = parseFrontmatter(raw);
      return {
        id,
        title: typeof data.title === "string" ? data.title : id,
        description:
          typeof data.description === "string" ? data.description : "",
        metadata,
        raw,
        body: content.trim(),
      };
    }),
  };
}

if (!fs.existsSync(dist)) {
  throw new Error(`Missing dist directory at ${dist}; run vite build first`);
}

const { config: docsConfig, pages } = loadPagesFromNav();
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

const fullParts = ["# Kontext Documentation", "", `> ${siteDescription}`, ""];

for (const page of pages) {
  const outPath = path.join(dist, page.metadata.rawPath.slice(1));
  fs.mkdirSync(path.dirname(outPath), { recursive: true });
  fs.writeFileSync(outPath, page.raw);

  const desc = page.description ? `: ${page.description}` : "";
  llmsLines.push(
    `- [${page.title}](https://docs.kontext.run${page.metadata.rawPath})${desc}`,
  );
  fullParts.push(`---\n\n# ${page.title}\n\n${page.body}\n`);
}

fs.writeFileSync(path.join(dist, "llms.txt"), `${llmsLines.join("\n")}\n`);
fs.writeFileSync(path.join(dist, "llms-full.txt"), `${fullParts.join("\n")}\n`);

console.log(
  `Wrote llms.txt, llms-full.txt, and ${pages.length} markdown mirrors into dist/`,
);
