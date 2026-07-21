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

  for (const id of navIds) {
    const metadata = requirePageMetadata(id);
    const srcFile = `${id}.md`;
    assert.deepEqual(metadata, {
      srcFile,
      routePath: id === "docs/index" ? "/docs" : `/${id}`,
      rawPath: `/raw/${srcFile}`,
      githubPath: srcFile,
    });
    assert.ok(fs.existsSync(path.join(repoRoot, metadata.githubPath)));
  }
  assert.equal(requirePageMetadata("docs/index").routePath, "/docs");
  assert.equal(requirePageMetadata("SPEC").rawPath, "/raw/SPEC.md");
  assert.throws(
    () => requirePageMetadata("docs/not-in-navigation"),
    /Missing page metadata/,
  );
});
