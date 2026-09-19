import fs from "node:fs";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const docsSite = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
);
const dist = path.join(docsSite, "dist");
const serverOut = path.join(docsSite, ".ssr");
const template = fs.readFileSync(path.join(dist, "index.html"), "utf8");
const serverEntry = path.join(serverOut, "entry-server.js");
const { render, routes } = await import(pathToFileURL(serverEntry).href);

function escapeAttribute(value) {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll('"', "&quot;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;");
}

function replaceMeta(html, { path: routePath, title, description }) {
  const pageTitle = `${title} · Kontext Docs`;
  const canonical = new URL(routePath, "https://docs.kontext.run").href;

  return html
    .replace(/<title>[\s\S]*?<\/title>/, `<title>${pageTitle}</title>`)
    .replace(
      /<meta\s+name="description"\s+content="[^"]*"\s*\/?>/,
      `<meta name="description" content="${escapeAttribute(description)}" />`,
    )
    .replace(
      /<link\s+rel="canonical"\s+href="[^"]*"\s*\/?>/,
      `<link rel="canonical" href="${canonical}" />`,
    )
    .replace(
      /<meta\s+property="og:title"\s+content="[^"]*"\s*\/?>/,
      `<meta property="og:title" content="${escapeAttribute(pageTitle)}" />`,
    )
    .replace(
      /<meta\s+property="og:description"\s+content="[^"]*"\s*\/?>/,
      `<meta property="og:description" content="${escapeAttribute(description)}" />`,
    )
    .replace(
      /<meta\s+property="og:url"\s+content="[^"]*"\s*\/?>/,
      `<meta property="og:url" content="${canonical}" />`,
    )
    .replace(
      /<meta\s+name="twitter:title"\s+content="[^"]*"\s*\/?>/,
      `<meta name="twitter:title" content="${escapeAttribute(pageTitle)}" />`,
    )
    .replace(
      /<meta\s+name="twitter:description"\s+content="[^"]*"\s*\/?>/,
      `<meta name="twitter:description" content="${escapeAttribute(description)}" />`,
    );
}

for (const route of routes) {
  const markup = render(route.path);
  const html = replaceMeta(
    template.replace('<div id="root"></div>', `<div id="root">${markup}</div>`),
    route,
  );
  const output = path.join(dist, route.path.replace(/^\//, ""), "index.html");
  fs.mkdirSync(path.dirname(output), { recursive: true });
  fs.writeFileSync(output, html);
}

fs.rmSync(serverOut, { recursive: true, force: true });
console.log(`Prerendered ${routes.length} docs routes`);
