import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";

// Self-hosted IBM Plex, replacing the fonts.googleapis.com <link> that used
// to live in index.html. The CSP's style-src/font-src only allow 'self',
// so a network stylesheet was silently refused and the whole UI fell back
// to system-ui — see tokens.css and design/Foundations.dc.html for why the
// typeface is normative, not decorative (mono alignment in particular).
// Loosening the CSP to add googleapis/gstatic was rejected: a desktop
// database client should not fetch typefaces over the network at launch.
// Only the weights the CSS actually applies are imported — Sans at 400
// (body default), 500 and 600; Mono at 400 and the 500 it inherits from
// `.btn`'s font-weight for the ⌘-hint spans — and only the latin subset,
// since the interface copy is English-only and the full charset (every
// script IBM Plex ships) would be pure dead weight in the bundle.
import "@fontsource/ibm-plex-sans/latin-400.css";
import "@fontsource/ibm-plex-sans/latin-500.css";
import "@fontsource/ibm-plex-sans/latin-600.css";
import "@fontsource/ibm-plex-mono/latin-400.css";
import "@fontsource/ibm-plex-mono/latin-500.css";

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
