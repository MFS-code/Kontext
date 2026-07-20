import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const docsSite = path.resolve(__dirname, "..");
const contentRoot = path.join(docsSite, "content");
const dist = path.join(docsSite, "dist");

const pages = [
  {
    title: "Introduction",
    description:
      "Kontext runs AI agents as Kubernetes workloads with Agent and AgentRun resources.",
    src: "docs/index.md",
    mdOut: "docs/index.md",
  },
  {
    title: "Quickstart",
    description:
      "Install Kontext from a published release and run a keyless echo AgentRun without cloning the repository.",
    src: "docs/quickstart.md",
    mdOut: "docs/quickstart.md",
  },
  {
    title: "Resource model",
    description: "How Agent and AgentRun map to familiar Kubernetes objects.",
    src: "docs/resources.md",
    mdOut: "docs/resources.md",
  },
  {
    title: "Service workload",
    description:
      "Deploy a persistent Service-mode Agent and watch the controller re-cast it after Pod deletion.",
    src: "docs/service-workload.md",
    mdOut: "docs/service-workload.md",
  },
  {
    title: "Operations",
    description:
      "Alpha support matrix, failure boundaries, identity, secrets, network, budgets, and troubleshooting.",
    src: "docs/operations.md",
    mdOut: "docs/operations.md",
  },
  {
    title: "Releases",
    description:
      "Version tags, GHCR images, digest-pinned install.yaml, upgrades, and uninstall.",
    src: "docs/releases.md",
    mdOut: "docs/releases.md",
  },
  {
    title: "Runtimes",
    description:
      "Echo, reference, and bring-your-own runtime roles and result capture paths.",
    src: "docs/runtimes.md",
    mdOut: "docs/runtimes.md",
  },
  {
    title: "Evaluations",
    description: "How kontext-eval grades AgentRuns outside the agent Pod.",
    src: "docs/evals.md",
    mdOut: "docs/evals.md",
  },
  {
    title: "API specification",
    description:
      "Agent, AgentRun, and runtime-image contract for kontext.dev/v1alpha1.",
    src: "SPEC.md",
    mdOut: "SPEC.md",
  },
  {
    title: "When not to use agents",
    description:
      "Prefer deterministic Jobs and scripts when model judgment is not justified.",
    src: "docs/when-not-to-use-agents.md",
    mdOut: "docs/when-not-to-use-agents.md",
  },
];

function stripFrontmatter(raw) {
  if (!raw.startsWith("---")) {
    return raw.trim();
  }
  const end = raw.indexOf("\n---", 3);
  if (end === -1) {
    return raw.trim();
  }
  return raw.slice(end + 4).trim();
}

if (!fs.existsSync(dist)) {
  throw new Error(`Missing dist directory at ${dist}; run vite build first`);
}

const llmsLines = [
  "# Kontext",
  "",
  "> Kubernetes-native control plane for running AI agents as production workloads.",
  "",
  "Full docs: https://docs.kontext.run/llms-full.txt",
  "",
  "## Pages",
  "",
];

const fullParts = [
  "# Kontext Documentation",
  "",
  "> Kubernetes-native control plane for running AI agents as production workloads.",
  "",
];

for (const page of pages) {
  const srcPath = path.join(contentRoot, page.src);
  const raw = fs.readFileSync(srcPath, "utf8");
  const body = stripFrontmatter(raw);
  const outPath = path.join(dist, "raw", page.mdOut);
  fs.mkdirSync(path.dirname(outPath), { recursive: true });
  fs.writeFileSync(outPath, raw);

  llmsLines.push(
    `- [${page.title}](https://docs.kontext.run/raw/${page.mdOut}): ${page.description}`,
  );
  fullParts.push(`---\n\n# ${page.title}\n\n${body}\n`);
}

fs.writeFileSync(path.join(dist, "llms.txt"), `${llmsLines.join("\n")}\n`);
fs.writeFileSync(path.join(dist, "llms-full.txt"), `${fullParts.join("\n")}\n`);

console.log(
  `Wrote llms.txt, llms-full.txt, and ${pages.length} markdown mirrors into dist/`,
);
