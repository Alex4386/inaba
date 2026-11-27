import { ThemeProvider } from "next-themes";
import { createRoot } from "react-dom/client";
import "@xterm/xterm/css/xterm.css";
import "./index.css";
import { Router } from "wouter";
import { App } from "./App";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { I18nProvider } from "@/lib/i18n";
import { applyCodeFont, applyUiFont, loadCodeFont } from "@/lib/settings";

// The part of the page not under the on-screen keyboard, for the app to fit in: Safari slides the
// page up under the keyboard rather than shrinking it, which took the composer down with it.
const visible = window.visualViewport;
if (visible) {
  const fit = () => {
    const style = document.documentElement.style;
    // Pinch-zoom shrinks the visual viewport too; the app keeps its size then (index.css's 100svh/0).
    if (Math.abs(visible.scale - 1) > 0.01) {
      style.removeProperty("--vvh");
      style.removeProperty("--vvt");
      return;
    }
    style.setProperty("--vvh", `${visible.height}px`);
    style.setProperty("--vvt", `${visible.offsetTop}px`);
  };
  visible.addEventListener("resize", fit);
  visible.addEventListener("scroll", fit);
  fit();
}

// The chosen code font applies before anything draws.
applyCodeFont();
applyUiFont();

// The page draws at once; the editor and terminal measure the code font again once it is in.
void loadCodeFont();
createRoot(document.getElementById("root")!).render(
  // The theme's dark variant is the `dark` class; "system" follows the OS setting.
  <ThemeProvider attribute="class" defaultTheme="system" enableSystem disableTransitionOnChange>
    <I18nProvider>
      <TooltipProvider>
        {/* The UI is served under /ui; its own addresses are below that. */}
        <Router base="/ui">
          <App />
        </Router>
        <Toaster />
      </TooltipProvider>
    </I18nProvider>
  </ThemeProvider>,
);
