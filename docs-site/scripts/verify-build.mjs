import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { pageMetadataById } from "../shared/docs.js";

const docsSite = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
);
const repoRoot = path.resolve(docsSite, "..");
const dist = path.join(docsSite, "dist");
const vercelConfig = JSON.parse(
  fs.readFileSync(path.join(docsSite, "vercel.json"), "utf8"),
);
const rewrites = new Map(
  vercelConfig.rewrites.map(({ source, destination }) => [
    source,
    destination,
  ]),
);
const spaCatchAll = vercelConfig.rewrites.find(
  ({ destination }) => destination === "/index.html",
);
assert.ok(spaCatchAll, "Vercel config has an SPA catch-all");
const spaCatchAllPattern = new RegExp(spaCatchAll.source);
assert.equal(spaCatchAllPattern.test("/robots.txt"), false);
assert.equal(spaCatchAllPattern.test("/sitemap.xml"), false);
assert.equal(spaCatchAllPattern.test("/docs/search"), true);

for (const [id, metadata] of pageMetadataById) {
  const source = fs.readFileSync(path.join(repoRoot, metadata.srcFile));
  const mirror = fs.readFileSync(
    path.join(dist, metadata.rawPath.replace(/^\//, "")),
  );
  assert.deepEqual(
    mirror,
    source,
    `${metadata.rawPath} does not byte-match ${metadata.srcFile}`,
  );

  const page = fs.readFileSync(
    path.join(dist, metadata.routePath.replace(/^\//, ""), "index.html"),
    "utf8",
  );
  const sourceText = source.toString("utf8");
  const title = sourceText.match(/^title:\s*(.+)$/m)?.[1];
  const description = sourceText.match(/^description:\s*(.+)$/m)?.[1];
  assert.ok(title, `${metadata.srcFile} has a title`);
  assert.ok(description, `${metadata.srcFile} has a description`);
  assert.ok(
    page.includes(`<title>${title} · Kontext Docs</title>`),
    `${metadata.routePath} has its route-specific title`,
  );
  assert.ok(
    page.includes(`name="description" content="${description}"`),
    `${metadata.routePath} has its route-specific description`,
  );
  assert.match(page, /<main class="content">[\s\S]*<h1>/);
  assert.ok(
    page.includes(`href="${metadata.routePath}"`),
    `${id} appears in prerendered navigation`,
  );
  assert.equal(
    rewrites.get(metadata.routePath),
    `${metadata.routePath}/index.html`,
    `${metadata.routePath} serves its prerendered HTML on Vercel`,
  );
}

const sitemap = fs.readFileSync(path.join(dist, "sitemap.xml"), "utf8");
const sitemapUrls = [
  ...sitemap.matchAll(/<loc>(https:\/\/docs\.kontext\.run\/[^<]*)<\/loc>/g),
].map((match) => match[1]);
const expectedSitemapUrls = [...pageMetadataById.values()].map(
  ({ routePath }) => new URL(routePath, "https://docs.kontext.run").href,
);
assert.deepEqual(sitemapUrls, expectedSitemapUrls);
assert.equal(new Set(sitemapUrls).size, pageMetadataById.size);

const robots = fs.readFileSync(path.join(dist, "robots.txt"), "utf8");
assert.equal(
  robots,
  [
    "User-agent: *",
    "Allow: /",
    "",
    "Sitemap: https://docs.kontext.run/sitemap.xml",
    "",
  ].join("\n"),
);

const llmsIndex = fs.readFileSync(path.join(dist, "llms.txt"), "utf8");
for (const page of ["task-workload", "scheduled-workload"]) {
  assert.match(
    llmsIndex,
    new RegExp(`https://docs\\.kontext\\.run/raw/docs/${page}\\.md`),
  );
}

const llmsFull = fs.readFileSync(path.join(dist, "llms-full.txt"), "utf8");
for (const term of [
  "Task",
  "Scheduled",
  "ScheduleSpec",
  "goalTemplate",
  "MutatingWebhookConfiguration",
  "failurePolicy: Fail",
]) {
  assert.ok(llmsFull.includes(term), `llms-full.txt is missing ${term}`);
}

console.log(
  `Verified ${pageMetadataById.size} prerendered routes, raw mirrors, and generated LLM corpora`,
);
