import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import {
  pageMetadataById,
  parseFrontmatter,
  requirePageMetadata,
} from "../shared/docs.js";

const repoRoot = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../..",
);

test("parseFrontmatter parses string values and preserves body whitespace", () => {
  const raw = [
    "---",
    'title: "A title: with punctuation"',
    "description: 'A description'",
    "ignored line",
    "---",
    "",
    "# Body",
    "",
  ].join("\n");

  assert.deepEqual(parseFrontmatter(raw), {
    data: {
      title: "A title: with punctuation",
      description: "A description",
    },
    content: "\n# Body\n",
  });
});

test("parseFrontmatter accepts CRLF and a closing delimiter at EOF", () => {
  assert.deepEqual(
    parseFrontmatter("---\r\ntitle: Windows\r\n---\r\nBody\r\n"),
    {
      data: { title: "Windows" },
      content: "Body\r\n",
    },
  );
  assert.deepEqual(parseFrontmatter("---\ntitle: Empty\n---"), {
    data: { title: "Empty" },
    content: "",
  });
});

test("parseFrontmatter leaves invalid boundaries untouched", () => {
  const invalidInputs = [
    "Plain Markdown\n---\n",
    "---not-an-opening-delimiter\ntitle: Wrong\n---\nBody",
    " ---\ntitle: Wrong\n---\nBody",
    "---\ntitle: Unclosed",
    "---\ntitle: Wrong\n----\nBody",
    "---\ntitle: Wrong\n--- trailing\nBody",
    "---",
  ];

  for (const raw of invalidInputs) {
    assert.deepEqual(parseFrontmatter(raw), { data: {}, content: raw });
  }
});

test("page metadata covers every navigation page with stable paths", () => {
  const docsConfig = JSON.parse(
    fs.readFileSync(path.join(repoRoot, "docs-nav.json"), "utf8"),
  );
  const navIds = docsConfig.navigation.groups.flatMap((group) => group.pages);

  assert.equal(new Set(navIds).size, navIds.length);
  assert.deepEqual([...pageMetadataById.keys()].sort(), [...navIds].sort());
  assert.deepEqual(Object.fromEntries(pageMetadataById), {
    "docs/index": {
      srcFile: "docs/index.md",
      routePath: "/docs",
      rawPath: "/raw/docs/index.md",
      githubPath: "docs/index.md",
    },
    "docs/quickstart": {
      srcFile: "docs/quickstart.md",
      routePath: "/docs/quickstart",
      rawPath: "/raw/docs/quickstart.md",
      githubPath: "docs/quickstart.md",
    },
    "docs/resources": {
      srcFile: "docs/resources.md",
      routePath: "/docs/resources",
      rawPath: "/raw/docs/resources.md",
      githubPath: "docs/resources.md",
    },
    "docs/service-workload": {
      srcFile: "docs/service-workload.md",
      routePath: "/docs/service-workload",
      rawPath: "/raw/docs/service-workload.md",
      githubPath: "docs/service-workload.md",
    },
    "docs/operations": {
      srcFile: "docs/operations.md",
      routePath: "/docs/operations",
      rawPath: "/raw/docs/operations.md",
      githubPath: "docs/operations.md",
    },
    "docs/releases": {
      srcFile: "docs/releases.md",
      routePath: "/docs/releases",
      rawPath: "/raw/docs/releases.md",
      githubPath: "docs/releases.md",
    },
    "docs/runtimes": {
      srcFile: "docs/runtimes.md",
      routePath: "/docs/runtimes",
      rawPath: "/raw/docs/runtimes.md",
      githubPath: "docs/runtimes.md",
    },
    "docs/evals": {
      srcFile: "docs/evals.md",
      routePath: "/docs/evals",
      rawPath: "/raw/docs/evals.md",
      githubPath: "docs/evals.md",
    },
    SPEC: {
      srcFile: "SPEC.md",
      routePath: "/SPEC",
      rawPath: "/raw/SPEC.md",
      githubPath: "SPEC.md",
    },
    "docs/when-not-to-use-agents": {
      srcFile: "docs/when-not-to-use-agents.md",
      routePath: "/docs/when-not-to-use-agents",
      rawPath: "/raw/docs/when-not-to-use-agents.md",
      githubPath: "docs/when-not-to-use-agents.md",
    },
  });

  for (const id of navIds) {
    const metadata = requirePageMetadata(id);
    assert.ok(fs.existsSync(path.join(repoRoot, metadata.githubPath)));
  }
  assert.throws(
    () => requirePageMetadata("docs/not-in-navigation"),
    /Missing page metadata/,
  );
});
