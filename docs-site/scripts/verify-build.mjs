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

for (const metadata of pageMetadataById.values()) {
  const source = fs.readFileSync(path.join(repoRoot, metadata.srcFile));
  const mirror = fs.readFileSync(
    path.join(dist, metadata.rawPath.replace(/^\//, "")),
  );
  assert.deepEqual(
    mirror,
    source,
    `${metadata.rawPath} does not byte-match ${metadata.srcFile}`,
  );
}

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
  `Verified ${pageMetadataById.size} raw mirrors and generated LLM corpora`,
);
