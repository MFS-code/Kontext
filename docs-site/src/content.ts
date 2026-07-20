import docsConfig from "../content/docs.json";
import { parseFrontmatter } from "./frontmatter";

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

function stripMintlifyComponents(markdown: string): string {
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

function fileToId(filePath: string): string {
  const normalized = filePath.replace(/\\/g, "/");
  if (normalized.endsWith("/SPEC.md") || normalized.endsWith("SPEC.md")) {
    return "SPEC";
  }
  const match = normalized.match(/\/content\/docs\/([^/]+)\.md$/);
  if (!match) {
    throw new Error(`Unexpected docs path: ${filePath}`);
  }
  return `docs/${match[1]}`;
}

function idToPath(id: string): string {
  if (id === "SPEC") {
    return "/SPEC";
  }
  if (id === "docs/index") {
    return "/docs";
  }
  return `/${id}`;
}

function parsePage(filePath: string, raw: string): DocPageMeta {
  const id = fileToId(filePath);
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
    path: idToPath(id),
    file: filePath,
    title,
    sidebarTitle,
    description,
    body: stripMintlifyComponents(content.trim()),
  };
}

const pagesById = new Map<string, DocPageMeta>();

for (const [filePath, raw] of Object.entries(modules)) {
  const page = parsePage(filePath, raw);
  pagesById.set(page.id, page);
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
        throw new Error(`docs.json references missing page: ${id}`);
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
    const haystack = `${page.title}\n${page.sidebarTitle}\n${page.description}\n${page.body}`.toLowerCase();
    return haystack.includes(q);
  });
}
