import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'

import App from './App'
import './styles.css'

// Sub2API's custom-page integration may append its own bearer token to an
// iframe URL. The invoice application never consumes or persists upstream
// credentials. Production Nginx strips them before serving the SPA; this is a
// second browser-side defense for direct or misconfigured entry URLs.
const currentURL = new URL(window.location.href)
let removedSensitiveParameter = false
for (const name of ['token', 'access_token', 'id_token', 'api_key', 'key']) {
  if (currentURL.searchParams.has(name)) {
    currentURL.searchParams.delete(name)
    removedSensitiveParameter = true
  }
}
if (removedSensitiveParameter) {
  window.history.replaceState(null, '', `${currentURL.pathname}${currentURL.search}${currentURL.hash}`)
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </React.StrictMode>,
)
