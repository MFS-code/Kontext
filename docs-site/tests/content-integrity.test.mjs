import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { pageMetadataById } from "../shared/docs.js";

const repoRoot = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../..",
);

function filesUnder(directory, extension, ignoredDirectories = new Set()) {
  const files = [];
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    const absolute = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      if (!ignoredDirectories.has(entry.name)) {
        files.push(...filesUnder(absolute, extension, ignoredDirectories));
      }
    } else if (entry.name.endsWith(extension)) {
      files.push(absolute);
    }
  }
  return files;
}

const markdownFiles = filesUnder(
  repoRoot,
  ".md",
  new Set([".git", "content", "dist", "node_modules"]),
);

const routeSources = new Map(
  [...pageMetadataById.values()].map((metadata) => [
    metadata.routePath,
    metadata.srcFile,
  ]),
);

test("public docs contain no stale mode language", () => {
  const publicFiles = [
    ...filesUnder(path.join(repoRoot, "docs"), ".md"),
    ...[
      "README.md",
      "SPEC.md",
      "SUPPORT.md",
      "CONTRIBUTING.md",
      "website/index.html",
      ".github/ISSUE_TEMPLATE/bug_report.yml",
    ].map((file) => path.join(repoRoot, file)),
  ];
  const forbidden = [
    /\bUnsupportedMode\b/,
    /\breserved\b/i,
    /Until Task mutation ships/i,
  ];

  for (const file of publicFiles) {
    const source = fs.readFileSync(file, "utf8");
    for (const pattern of forbidden) {
      assert.doesNotMatch(
        source,
        pattern,
        `${path.relative(repoRoot, file)} contains stale public language`,
      );
    }
  }
});

test("internal Markdown links resolve", () => {
  const failures = [];
  const linkPattern = /!?\[[^\]]*]\(([^)\s]+)(?:\s+["'][^"']*["'])?\)/g;

  for (const file of markdownFiles) {
    const source = fs.readFileSync(file, "utf8");
    for (const match of source.matchAll(linkPattern)) {
      const rawTarget = match[1].replace(/^<|>$/g, "");
      if (rawTarget.startsWith("#") || /^[a-z][a-z0-9+.-]*:/i.test(rawTarget)) {
        continue;
      }

      const pathname = decodeURIComponent(
        rawTarget.split("#", 1)[0].split("?", 1)[0],
      );
      if (!pathname) {
        continue;
      }

      let target;
      if (pathname.startsWith("/")) {
        const sourceFile = routeSources.get(pathname);
        if (!sourceFile) {
          failures.push(
            `${path.relative(repoRoot, file)} -> unknown docs route ${pathname}`,
          );
          continue;
        }
        target = path.join(repoRoot, sourceFile);
      } else {
        target = path.resolve(path.dirname(file), pathname);
      }

      if (!fs.existsSync(target)) {
        failures.push(`${path.relative(repoRoot, file)} -> ${rawTarget}`);
      }
    }
  }

  assert.deepEqual(failures, []);
});

test("referenced example paths exist", () => {
  const failures = [];
  const examplePattern = /deploy\/examples\/v1alpha1\/[A-Za-z0-9._/-]+/g;

  for (const file of markdownFiles) {
    const source = fs.readFileSync(file, "utf8");
    for (const match of source.matchAll(examplePattern)) {
      const relative = match[0].replace(/[.,;:]+$/, "");
      if (!fs.existsSync(path.join(repoRoot, relative))) {
        failures.push(`${path.relative(repoRoot, file)} -> ${relative}`);
      }
    }
  }

  assert.deepEqual(failures, []);
});
