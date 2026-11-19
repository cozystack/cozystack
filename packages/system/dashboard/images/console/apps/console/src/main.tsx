import { StrictMode } from "react"
import { createRoot } from "react-dom/client"
import { BrowserRouter } from "react-router"
import { K8sProvider } from "@cozystack/k8s-client"
import "./index.css"
import App from "./App.tsx"
import { loadConfig, loadUsername } from "./lib/config.ts"

Promise.all([loadConfig(), loadUsername()]).then(([config, username]) => {
  if (config.titleText) document.title = config.titleText

  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <K8sProvider>
        <BrowserRouter>
          <App config={config} username={username} />
        </BrowserRouter>
      </K8sProvider>
    </StrictMode>,
  )
})
