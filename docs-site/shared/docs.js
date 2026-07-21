// @ts-check

import docsConfig from "../../docs-nav.json" with { type: "json" };

/**
 * @typedef {Readonly<{
 *   srcFile: string;
 *   routePath: string;
 *   rawPath: string;
 *   githubPath: string;
 * }>} PageMetadata
 */

const pageIds = docsConfig.navigation.groups.flatMap((group) => group.pages);

/**
 * @param {string} id
 * @returns {PageMetadata}
 */
function derivePageMetadata(id) {
  const srcFile = `${id}.md`;
  return {
    srcFile,
    routePath: id === "docs/index" ? "/docs" : `/${id}`,
    rawPath: `/raw/${srcFile}`,
    githubPath: srcFile,
  };
}

/** @type {ReadonlyMap<string, PageMetadata>} */
export const pageMetadataById = new Map(
  pageIds.map((id) => [id, derivePageMetadata(id)]),
);

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
