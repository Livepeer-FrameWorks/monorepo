import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import "./index.css";
// CSS: import core directly (Vite alias resolves to source file)
import "@livepeer-frameworks/player-core/player.css";
import "@livepeer-frameworks/player-core/themes/light.css";
import "@livepeer-frameworks/player-core/themes/neutral-dark.css";
import "@livepeer-frameworks/streamcrafter-core/streamcrafter.css";

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
