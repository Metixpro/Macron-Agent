import { createRoot } from "react-dom/client";
import { applyTheme } from "./actions";
import { App } from "./App";
import { getState } from "./store";
import "./styles.css";

applyTheme(getState().theme);
createRoot(document.getElementById("root") as HTMLElement).render(<App />);
