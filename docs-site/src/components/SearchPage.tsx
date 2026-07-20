import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { searchPages } from "../content";

export function SearchPage() {
  const [query, setQuery] = useState("");

  useEffect(() => {
    document.title = "Search · Kontext Docs";
  }, []);

  const results = useMemo(() => searchPages(query), [query]);

  return (
    <article className="doc">
      <h1>Search</h1>
      <p className="lede">
        Find a page by title, description, or body text. Same idea as a docs
        index — no external search service.
      </p>
      <label className="search-label" htmlFor="docs-search">
        Query
      </label>
      <input
        id="docs-search"
        className="search-input"
        type="search"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        placeholder="AgentRun, budgets, install…"
        autoFocus
      />
      {query.trim() ? (
        <ul className="search-results">
          {results.length === 0 ? (
            <li className="muted">No pages matched.</li>
          ) : (
            results.map((page) => (
              <li key={page.id}>
                <Link to={page.path}>{page.title}</Link>
                {page.description ? (
                  <p className="muted">{page.description}</p>
                ) : null}
              </li>
            ))
          )}
        </ul>
      ) : (
        <p className="muted">Type a few characters to search.</p>
      )}
    </article>
  );
}
