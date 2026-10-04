import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import './styles/tokens.css'
import './styles/base.css'
import App from './App.tsx'
import { createApiClient } from './api/index.ts'

const root = document.getElementById('root')
if (!root) {
  throw new Error('MealQuest: #root element missing from index.html')
}

const api = createApiClient(window.location)

createRoot(root).render(
  <StrictMode>
    <BrowserRouter>
      <App api={api} />
    </BrowserRouter>
  </StrictMode>,
)
