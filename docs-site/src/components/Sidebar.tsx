import { NavLink } from "react-router-dom";
import { navGroups } from "../content";

export function Sidebar() {
  return (
    <aside className="sidebar" aria-label="Documentation">
      <p className="sidebar-label">Docs</p>
      {navGroups.map((group) => (
        <div key={group.group} className="nav-group">
          <p className="nav-group-title">{group.group}</p>
          <ul>
            {group.pages.map((page) => (
              <li key={page.id}>
                <NavLink
                  to={page.path}
                  end={page.path === "/docs"}
                  className={({ isActive }) =>
                    isActive ? "nav-link active" : "nav-link"
                  }
                >
                  {page.sidebarTitle}
                </NavLink>
              </li>
            ))}
          </ul>
        </div>
      ))}
    </aside>
  );
}
