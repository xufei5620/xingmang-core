import React from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router'

import App from './App'
import './styles.css'
import './user-shell.css'

// Customer entry URLs must not retain upstream credentials. Keep this
// browser-side defense for direct or misconfigured links.
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
      <div className="invoice-workspace"><App /></div>
    </BrowserRouter>
  </React.StrictMode>,
)
