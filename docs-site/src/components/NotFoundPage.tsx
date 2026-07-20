import { useEffect } from "react";
import { Link } from "react-router-dom";

export function NotFoundPage() {
  useEffect(() => {
    document.title = "Not found · Kontext Docs";
  }, []);

  return (
    <article className="doc">
      <h1>Not found</h1>
      <p>
        No page at this path. Start at <Link to="/docs">Introduction</Link> or{" "}
        <Link to="/docs/search">Search</Link>.
      </p>
    </article>
  );
}
