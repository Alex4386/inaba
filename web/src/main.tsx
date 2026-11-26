import { ThemeProvider } from "next-themes";
import { createRoot } from "react-dom/client";
import "@xterm/xterm/css/xterm.css";
import "./index.css";
import { Router } from "wouter";
import { App } from "./App";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { I18nProvider } from "@/lib/i18n";
import { applyCodeFont, applyUiFont } from "@/lib/settings";

// The chosen code font applies before anything draws.
applyCodeFont();
applyUiFont();

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
