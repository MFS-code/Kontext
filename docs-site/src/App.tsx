import { Navigate, Route, Routes } from "react-router-dom";
import { Layout } from "./components/Layout";
import { DocPage } from "./components/DocPage";
import { SearchPage } from "./components/SearchPage";
import { NotFoundPage } from "./components/NotFoundPage";
import { getPageByPath, navGroups } from "./content";

const docPaths = navGroups.flatMap((group) =>
  group.pages.map((page) => page.path),
);

export function App() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<Navigate to="/docs" replace />} />
        <Route path="/docs/search" element={<SearchPage />} />
        {docPaths.map((path) => (
          <Route
            key={path}
            path={path}
            element={<DocPage page={getPageByPath(path)} />}
          />
        ))}
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
  );
}
