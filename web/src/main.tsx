import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import TelegramMiniApp from './TelegramMiniApp'
import './styles.css'
import './design-system.css'

const rootElement = document.getElementById('root')

if (!rootElement) {
  throw new Error('Не найден корневой элемент #root')
}

const RootApp = window.location.pathname === '/miniapp' || window.location.pathname.startsWith('/miniapp/')
  ? TelegramMiniApp
  : App

createRoot(rootElement).render(
  <StrictMode>
    <RootApp />
  </StrictMode>,
)

