import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import "./index.css";
// Vite aliases resolve these to the package sources.
import "@livepeer-frameworks/player-core/player.css";
import "@livepeer-frameworks/player-core/themes/light.css";
import "@livepeer-frameworks/player-core/themes/neutral-dark.css";
import "@livepeer-frameworks/streamcrafter-core/streamcrafter.css";
import "@livepeer-frameworks/player-tester/tester.css";

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
