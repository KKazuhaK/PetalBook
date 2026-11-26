import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './lib/viewAsBoot'
import './index.css'
import App from './App.tsx'
import { installStaleChunkHandler } from './lib/staleChunk'

installStaleChunkHandler()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
