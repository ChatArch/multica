import ReactDOM from "react-dom/client";
import App from "./App";
// Inter variable font covers all weights (100-900) in a single file.
// Geist Mono kept as-is for code blocks; CJK is handled by system font fallback
// (see globals.css --font-sans chain). Keep font stack in sync with apps/web/app/layout.tsx.
import "@fontsource-variable/inter";
// Editorial serif — matches web's next/font Source_Serif_4. Loaded app-wide so
// onboarding headings and any future editorial surface can use `font-serif`
// (see tokens.css @theme inline). Variable font = one file covers all weights.
import "@fontsource-variable/source-serif-4";
import "@fontsource-variable/source-serif-4/wght-italic.css";
// 400/500/700 mirror the weights shared components actually ask for. Web gets
// Geist Mono from next/font as a variable face (every weight available), so a
// weight missing here renders lighter on desktop than on web for the same
// component — `font-mono font-medium` in packages/ui/components/ui/chart.tsx is
// the case that surfaced it. Add the file here when a new mono weight appears.
import "@fontsource/geist-mono/400.css";
import "@fontsource/geist-mono/500.css";
import "@fontsource/geist-mono/700.css";
import "./globals.css";

// react-grab: dev-only element inspector. Hold ⌘C (Mac) / Ctrl+C and click any
// element to copy its source path + line + component stack for pasting to an AI.
// Opt-in per developer: only loads when VITE_REACT_GRAB is set in a local,
// gitignored apps/desktop/.env.development.local — it never activates for anyone
// else, and the whole branch is tree-shaken out of production builds. The web app
// wires the same tool via next/script in apps/web/app/layout.tsx.
// See https://www.react-grab.com/
if (import.meta.env.DEV && import.meta.env.VITE_REACT_GRAB) {
  const grab = document.createElement("script");
  grab.src = "//unpkg.com/react-grab/dist/index.global.js";
  grab.crossOrigin = "anonymous";
  document.head.appendChild(grab);
}

ReactDOM.createRoot(document.getElementById("root")!).render(<App />);
