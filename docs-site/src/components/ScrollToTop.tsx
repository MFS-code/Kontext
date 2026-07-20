import { useEffect } from "react";
import { useLocation } from "react-router-dom";

// React Router swaps route content in place without resetting window
// scroll, so a reader deep in one page would land mid-page on the next.
export function ScrollToTop() {
  const { pathname } = useLocation();

  useEffect(() => {
    window.scrollTo(0, 0);
  }, [pathname]);

  return null;
}
