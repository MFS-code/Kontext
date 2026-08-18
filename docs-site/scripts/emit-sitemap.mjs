import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { pageMetadataById } from "../shared/docs.js";

const docsSite = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
);
const dist = path.join(docsSite, "dist");
const origin = "https://docs.kontext.run";
const urls = [...pageMetadataById.values()].map(
  ({ routePath }) => new URL(routePath, origin).href,
);
const sitemap = [
  '<?xml version="1.0" encoding="UTF-8"?>',
  '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">',
  ...urls.map((url) => `  <url><loc>${url}</loc></url>`),
  "</urlset>",
  "",
].join("\n");

fs.writeFileSync(path.join(dist, "sitemap.xml"), sitemap);
console.log(`Generated sitemap.xml with ${urls.length} docs routes`);
