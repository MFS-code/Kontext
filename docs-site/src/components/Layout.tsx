import { NavLink, Outlet } from "react-router-dom";
import { Sidebar } from "./Sidebar";

export function Layout() {
  return (
    <div className="shell">
      <header className="topbar">
        <div className="topbar-inner">
          <a className="brand" href="https://kontext.run">
            kontext
          </a>
          <nav className="topnav" aria-label="Site">
            <NavLink to="/docs/search">Search</NavLink>
            <a href="https://github.com/MFS-code/Kontext">GitHub</a>
            <a href="https://kontext.run">Website</a>
          </nav>
        </div>
      </header>
      <div className="frame">
        <Sidebar />
        <main className="content">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
