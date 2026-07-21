import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
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

function trackedFiles(pathspecs) {
  return execFileSync("git", ["ls-files", "--", ...pathspecs], {
    cwd: repoRoot,
    encoding: "utf8",
  })
    .trim()
    .split("\n")
    .filter(Boolean)
    .map((file) => path.join(repoRoot, file));
}

const markdownFiles = trackedFiles(["*.md"]);

const releaseVersionSource = fs.readFileSync(
  path.join(repoRoot, "docs/releases.md"),
  "utf8",
);
const releaseVersionMatch = releaseVersionSource.match(
  /^The current public release is `(v\d+\.\d+\.\d+(?:-[0-9A-Za-z]+(?:\.[0-9A-Za-z]+)*)?)`\./m,
);
assert.ok(releaseVersionMatch, "docs/releases.md declares the public release");
const releaseVersion = releaseVersionMatch[1];

const releaseVersionFiles = [
  ...trackedFiles(["docs/*.md"]),
  ...[
    "README.md",
    "SECURITY.md",
    "website/index.html",
    "deploy/examples/v1alpha1/README.md",
    ".github/ISSUE_TEMPLATE/bug_report.yml",
  ].map((file) => path.join(repoRoot, file)),
];

const routeSources = new Map(
  [...pageMetadataById.values()].map((metadata) => [
    metadata.routePath,
    metadata.srcFile,
  ]),
);

function decodeLinkPath(rawTarget) {
  const encodedPathname = rawTarget.split("#", 1)[0].split("?", 1)[0];
  try {
    return { ok: true, pathname: decodeURIComponent(encodedPathname) };
  } catch (error) {
    if (!(error instanceof URIError)) {
      throw error;
    }
    return {
      ok: false,
      reason: "malformed percent escape in link target",
    };
  }
}

function collectInternalLinkFailures({ documents, root, routes }) {
  const failures = [];
  const linkPattern = /!?\[[^\]]*]\(([^)\s]+)(?:\s+["'][^"']*["'])?\)/g;

  for (const { file, source } of documents) {
    for (const match of source.matchAll(linkPattern)) {
      const rawTarget = match[1].replace(/^<|>$/g, "");
      if (rawTarget.startsWith("#") || /^[a-z][a-z0-9+.-]*:/i.test(rawTarget)) {
        continue;
      }

      const decoded = decodeLinkPath(rawTarget);
      if (!decoded.ok) {
        failures.push(
          `${path.relative(root, file)} -> ${rawTarget}: ${decoded.reason}`,
        );
        continue;
      }
      if (!decoded.pathname) {
        continue;
      }

      let target;
      if (decoded.pathname.startsWith("/")) {
        const sourceFile = routes.get(decoded.pathname);
        if (!sourceFile) {
          failures.push(
            `${path.relative(root, file)} -> unknown docs route ${decoded.pathname}`,
          );
          continue;
        }
        target = path.join(root, sourceFile);
      } else {
        target = path.resolve(path.dirname(file), decoded.pathname);
      }

      if (!fs.existsSync(target)) {
        failures.push(`${path.relative(root, file)} -> ${rawTarget}`);
      }
    }
  }

  return failures;
}

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

test("public release references use the declared version", () => {
  const releaseVersionPattern =
    /v\d+\.\d+\.\d+(?:-[0-9A-Za-z]+(?:\.[0-9A-Za-z]+)*)?/g;
  const echoImagePattern =
    /ghcr\.io\/mfs-code\/kontext-echo:([A-Za-z0-9._-]+)/g;

  for (const file of releaseVersionFiles) {
    const source = fs.readFileSync(file, "utf8");
    for (const match of source.matchAll(releaseVersionPattern)) {
      const prefix = source.slice(Math.max(0, match.index - 32), match.index);
      if (/Kubernetes\s*$/.test(prefix)) {
        continue;
      }
      assert.equal(
        match[0],
        releaseVersion,
        `${path.relative(repoRoot, file)} uses release version ${match[0]}`,
      );
    }
    for (const match of source.matchAll(echoImagePattern)) {
      assert.equal(
        match[1],
        releaseVersion,
        `${path.relative(repoRoot, file)} uses kontext-echo tag ${match[1]}`,
      );
    }
  }
});

test("internal Markdown links resolve", () => {
  const failures = collectInternalLinkFailures({
    documents: markdownFiles.map((file) => ({
      file,
      source: fs.readFileSync(file, "utf8"),
    })),
    root: repoRoot,
    routes: routeSources,
  });

  assert.deepEqual(failures, []);
});

test("link target decoding preserves encoded path characters", () => {
  assert.deepEqual(decodeLinkPath("notes%20complete.md?raw=1#summary"), {
    ok: true,
    pathname: "notes complete.md",
  });
  assert.deepEqual(decodeLinkPath("chapter%23one.md#heading"), {
    ok: true,
    pathname: "chapter#one.md",
  });
  assert.deepEqual(decodeLinkPath("query%3Fname.md?download=1"), {
    ok: true,
    pathname: "query?name.md",
  });
  assert.deepEqual(decodeLinkPath("notes-100%.md"), {
    ok: false,
    reason: "malformed percent escape in link target",
  });
});

test("link failures aggregate malformed and missing targets", (context) => {
  const fixtureRoot = fs.mkdtempSync(
    path.join(path.dirname(fileURLToPath(import.meta.url)), "links-"),
  );
  context.after(() => fs.rmSync(fixtureRoot, { recursive: true, force: true }));

  for (const file of ["notes complete.md", "chapter#one.md", "query?name.md"]) {
    fs.writeFileSync(path.join(fixtureRoot, file), "");
  }
  const fixtureFile = path.join(fixtureRoot, "fixture.md");
  const source = [
    "[literal percent](notes-100%.md)",
    "[valid encoded path](notes%20complete.md?raw=1#summary)",
    "[encoded fragment marker](chapter%23one.md#heading)",
    "[encoded query marker](query%3Fname.md?download=1)",
    "[first missing](missing-one.md)",
    "[second missing](missing-two.md)",
    "[anchor](#local)",
    "[external](https://example.com/notes-100%.md)",
    "[mail](mailto:docs@example.com)",
    "[raw mirror](https://docs.kontext.run/raw/docs/index.md)",
    "[GitHub source](https://github.com/MFS-code/Kontext/blob/main/README.md)",
  ].join("\n");

  assert.deepEqual(
    collectInternalLinkFailures({
      documents: [{ file: fixtureFile, source }],
      root: fixtureRoot,
      routes: new Map(),
    }),
    [
      "fixture.md -> notes-100%.md: malformed percent escape in link target",
      "fixture.md -> missing-one.md",
      "fixture.md -> missing-two.md",
    ],
  );
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
