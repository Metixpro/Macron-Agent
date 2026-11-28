import { createRoot } from "react-dom/client";
import { applyDensity, applyMotion, applyTheme } from "./actions";
import { App } from "./App";
import { applyLang } from "./i18n";
import { getState } from "./store";
import "./styles/index.css";

const initial = getState();
applyTheme(initial.theme);
applyDensity(initial.density);
applyMotion(initial.motion);
applyLang(initial.lang);
createRoot(document.getElementById("root") as HTMLElement).render(<App />);
