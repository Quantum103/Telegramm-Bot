import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import MiniApp from './MiniApp'

const rootElement = document.getElementById('root')

if (!rootElement) {
  throw new Error('Не найден корневой элемент #root')
}

createRoot(rootElement).render(
  <StrictMode>
    <MiniApp />
  </StrictMode>,
)
