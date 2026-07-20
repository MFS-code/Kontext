import docsConfig from "../content/docs-nav.json";
import { pageMetadataById, parseFrontmatter } from "../shared/docs.js";

export type DocPageMeta = {
  id: string;
  path: string;
  file: string;
  title: string;
  sidebarTitle: string;
  description: string;
  body: string;
};

export type NavGroup = {
  group: string;
  pages: DocPageMeta[];
};

const docsModules = import.meta.glob("../content/docs/*.md", {
  query: "?raw",
  import: "default",
  eager: true,
}) as Record<string, string>;

const specModules = import.meta.glob("../content/SPEC.md", {
  query: "?raw",
  import: "default",
  eager: true,
}) as Record<string, string>;

const modules = { ...docsModules, ...specModules };

function stripNoteCallouts(markdown: string): string {
  return markdown.replace(
    /<Note>\s*([\s\S]*?)\s*<\/Note>/g,
    (_match, body: string) => {
      const lines = body
        .trim()
        .split("\n")
        .map((line) => `> ${line}`)
        .join("\n");
      return `> **Note**\n${lines}`;
    },
  );
}

function parsePage({
  id,
  filePath,
  path,
  raw,
}: {
  id: string;
  filePath: string;
  path: string;
  raw: string;
}): DocPageMeta {
  const { data, content } = parseFrontmatter(raw);
  const title =
    typeof data.title === "string"
      ? data.title
      : id === "SPEC"
        ? "API specification"
        : id;
  const sidebarTitle =
    typeof data.sidebarTitle === "string" ? data.sidebarTitle : title;
  const description =
    typeof data.description === "string" ? data.description : "";

  return {
    id,
    path,
    file: filePath,
    title,
    sidebarTitle,
    description,
    body: stripNoteCallouts(content.trim()),
  };
}

const pagesById = new Map<string, DocPageMeta>();

for (const [id, metadata] of pageMetadataById) {
  const filePath = `../content/${metadata.srcFile}`;
  const raw = modules[filePath];
  if (raw === undefined) {
    throw new Error(`Missing docs source: ${metadata.srcFile}`);
  }
  pagesById.set(id, parsePage({ id, filePath, path: metadata.routePath, raw }));
}

export const allPages: DocPageMeta[] = [...pagesById.values()].sort((a, b) =>
  a.path.localeCompare(b.path),
);

export const navGroups: NavGroup[] = docsConfig.navigation.groups.map(
  (group) => ({
    group: group.group,
    pages: group.pages.map((id) => {
      const page = pagesById.get(id);
      if (!page) {
        throw new Error(`docs-nav.json references missing page: ${id}`);
      }
      return page;
    }),
  }),
);

export function getPageByPath(path: string): DocPageMeta | undefined {
  return allPages.find((page) => page.path === path);
}

export function searchPages(query: string): DocPageMeta[] {
  const q = query.trim().toLowerCase();
  if (!q) {
    return [];
  }
  return allPages.filter((page) => {
    const haystack =
      `${page.title}\n${page.sidebarTitle}\n${page.description}\n${page.body}`.toLowerCase();
    return haystack.includes(q);
  });
}
