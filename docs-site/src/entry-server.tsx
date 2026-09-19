import { renderToString } from "react-dom/server";
import { StaticRouter } from "react-router-dom";
import { App } from "./App";
import { allPages } from "./content";

export const routes = allPages.map(({ path, title, description }) => ({
  path,
  title,
  description,
}));

export function render(path: string): string {
  return renderToString(
    <StaticRouter location={path}>
      <App />
    </StaticRouter>,
  );
}
