import { useCallback, useEffect, useState } from 'react'

type ConnectionState = 'idle' | 'checking' | 'connected' | 'error'

const apiBaseURL = (import.meta.env.VITE_API_BASE_URL ?? '').replace(/\/+$/, '')

async function checkBackend(): Promise<void> {
  const response = await fetch(`${apiBaseURL}/health/ready`, {
    headers: { Accept: 'application/json' },
  })

  if (!response.ok) {
    throw new Error(`Backend ответил HTTP ${response.status}`)
  }

  const body: unknown = await response.json()
  if (
    typeof body !== 'object' ||
    body === null ||
    !('status' in body) ||
    body.status !== 'ok'
  ) {
    throw new Error('Backend вернул неожиданный ответ')
  }
}

function App() {
  const [connectionState, setConnectionState] = useState<ConnectionState>('idle')
  const [errorMessage, setErrorMessage] = useState('')
  const [checkedAt, setCheckedAt] = useState<string | null>(null)

  const verifyConnection = useCallback(async () => {
    setConnectionState('checking')
    setErrorMessage('')

    try {
      await checkBackend()
      setConnectionState('connected')
      setCheckedAt(new Date().toLocaleTimeString('ru-RU'))
    } catch (error) {
      setConnectionState('error')
      setErrorMessage(
        error instanceof Error
          ? error.message
          : 'Не удалось проверить соединение с backend',
      )
    }
  }, [])

  useEffect(() => {
    void verifyConnection()
  }, [verifyConnection])

  const stateLabel: Record<ConnectionState, string> = {
    idle: 'Не проверено',
    checking: 'Проверяем…',
    connected: 'Backend и PostgreSQL доступны',
    error: 'Не удалось подключиться',
  }

  return (
    <main className="page-shell">
      <section className="connection-panel" aria-labelledby="page-title">
        <p className="eyebrow">Этап 1 · Фундамент проекта</p>
        <h1 id="page-title">Библейская обучающая платформа</h1>
        <p className="intro">
          Минимальная тестовая страница. Здесь проверяется соединение frontend,
          backend и PostgreSQL; функции обучения пока не подключены.
        </p>

        <div
          className={`connection-status connection-status--${connectionState}`}
          role="status"
          aria-live="polite"
        >
          <span className="status-indicator" aria-hidden="true" />
          <span>{stateLabel[connectionState]}</span>
        </div>

        {connectionState === 'error' && (
          <p className="error-message">{errorMessage}</p>
        )}
        {connectionState === 'connected' && checkedAt && (
          <p className="checked-at">Последняя успешная проверка: {checkedAt}</p>
        )}

        <button
          className="check-button"
          type="button"
          onClick={() => void verifyConnection()}
          disabled={connectionState === 'checking'}
        >
          {connectionState === 'checking' ? 'Проверяем…' : 'Проверить соединение'}
        </button>
        <p className="endpoint-note">
          Проверяется <code>GET /health/ready</code>: ответ успешен, если backend
          доступен и может обратиться к PostgreSQL.
        </p>
      </section>
      <footer>Локальная версия для разработки · административная авторизация ещё не реализована</footer>
    </main>
  )
}

export default App

