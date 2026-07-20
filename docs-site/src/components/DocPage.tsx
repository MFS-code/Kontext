import { useEffect } from "react";
import { Link } from "react-router-dom";
import { requirePageMetadata } from "../../shared/docs.js";
import type { DocPageMeta } from "../content";
import { Markdown } from "./Markdown";

type DocPageProps = {
  page: DocPageMeta | undefined;
};

export function DocPage({ page }: DocPageProps) {
  useEffect(() => {
    if (!page) {
      document.title = "Not found · Kontext Docs";
      return;
    }
    document.title = `${page.title} · Kontext Docs`;
    const description = document.querySelector('meta[name="description"]');
    if (description && page.description) {
      description.setAttribute("content", page.description);
    }
  }, [page]);

  if (!page) {
    return (
      <article>
        <h1>Page not found</h1>
        <p>
          That page is not in the docs. Try <Link to="/docs">Introduction</Link>{" "}
          or <Link to="/docs/search">Search</Link>.
        </p>
      </article>
    );
  }

  const source = requirePageMetadata(page.id);

  return (
    <article className="doc">
      <p className="eyebrow">{page.description}</p>
      <Markdown source={page.body} />
      <p className="source-link">
        <a
          href={`https://github.com/MFS-code/Kontext/blob/main/${source.githubPath}`}
        >
          Edit on GitHub
        </a>
        {" · "}
        <a href={source.rawPath}>Raw Markdown</a>
      </p>
    </article>
  );
}
