// @ts-check

/**
 * @typedef {Readonly<{
 *   srcFile: string;
 *   routePath: string;
 *   rawPath: string;
 *   githubPath: string;
 * }>} PageMetadata
 */

/** @type {Array<[string, PageMetadata]>} */
const pageMetadataEntries = [
  [
    "docs/index",
    {
      srcFile: "docs/index.md",
      routePath: "/docs",
      rawPath: "/raw/docs/index.md",
      githubPath: "docs/index.md",
    },
  ],
  [
    "docs/quickstart",
    {
      srcFile: "docs/quickstart.md",
      routePath: "/docs/quickstart",
      rawPath: "/raw/docs/quickstart.md",
      githubPath: "docs/quickstart.md",
    },
  ],
  [
    "docs/resources",
    {
      srcFile: "docs/resources.md",
      routePath: "/docs/resources",
      rawPath: "/raw/docs/resources.md",
      githubPath: "docs/resources.md",
    },
  ],
  [
    "docs/service-workload",
    {
      srcFile: "docs/service-workload.md",
      routePath: "/docs/service-workload",
      rawPath: "/raw/docs/service-workload.md",
      githubPath: "docs/service-workload.md",
    },
  ],
  [
    "docs/operations",
    {
      srcFile: "docs/operations.md",
      routePath: "/docs/operations",
      rawPath: "/raw/docs/operations.md",
      githubPath: "docs/operations.md",
    },
  ],
  [
    "docs/releases",
    {
      srcFile: "docs/releases.md",
      routePath: "/docs/releases",
      rawPath: "/raw/docs/releases.md",
      githubPath: "docs/releases.md",
    },
  ],
  [
    "docs/runtimes",
    {
      srcFile: "docs/runtimes.md",
      routePath: "/docs/runtimes",
      rawPath: "/raw/docs/runtimes.md",
      githubPath: "docs/runtimes.md",
    },
  ],
  [
    "docs/evals",
    {
      srcFile: "docs/evals.md",
      routePath: "/docs/evals",
      rawPath: "/raw/docs/evals.md",
      githubPath: "docs/evals.md",
    },
  ],
  [
    "SPEC",
    {
      srcFile: "SPEC.md",
      routePath: "/SPEC",
      rawPath: "/raw/SPEC.md",
      githubPath: "SPEC.md",
    },
  ],
  [
    "docs/when-not-to-use-agents",
    {
      srcFile: "docs/when-not-to-use-agents.md",
      routePath: "/docs/when-not-to-use-agents",
      rawPath: "/raw/docs/when-not-to-use-agents.md",
      githubPath: "docs/when-not-to-use-agents.md",
    },
  ],
];

/** @type {ReadonlyMap<string, PageMetadata>} */
export const pageMetadataById = new Map(pageMetadataEntries);

/**
 * @param {string} id
 * @returns {PageMetadata}
 */
export function requirePageMetadata(id) {
  const metadata = pageMetadataById.get(id);
  if (!metadata) {
    throw new Error(`Missing page metadata for: ${id}`);
  }
  return metadata;
}

/**
 * Parse the small string-only frontmatter subset used by the docs.
 *
 * Delimiters must be bare `---` lines. If either boundary is invalid or the
 * closing delimiter is missing, the input is returned unchanged.
 *
 * @param {string} raw
 * @returns {{ data: Record<string, unknown>; content: string }}
 */
export function parseFrontmatter(raw) {
  const opening = /^---(?:\r?\n|$)/.exec(raw);
  if (!opening) {
    return { data: {}, content: raw };
  }

  const remainder = raw.slice(opening[0].length);
  const closing = /^---(?:\r?\n|$)/m.exec(remainder);
  if (!closing) {
    return { data: {}, content: raw };
  }

  const block = remainder.slice(0, closing.index);
  const content = remainder.slice(closing.index + closing[0].length);
  /** @type {Record<string, unknown>} */
  const data = {};

  for (const line of block.split(/\r?\n/)) {
    const separator = line.indexOf(":");
    if (separator === -1) {
      continue;
    }

    const key = line.slice(0, separator).trim();
    let value = line.slice(separator + 1).trim();
    if (
      (value.startsWith('"') && value.endsWith('"')) ||
      (value.startsWith("'") && value.endsWith("'"))
    ) {
      value = value.slice(1, -1);
    }
    data[key] = value;
  }

  return { data, content };
}
