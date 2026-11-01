import { useEffect, useMemo, useState, type FormEvent } from 'react'
import {
  listStaffConversations, listStaffConversationMessages, markStaffConversationRead, sendStaffConversationMessage,
  type StaffConversationMessage, type StaffConversationSummary, type StaffIdentity,
} from './api'
import './communications.css'

const messageOf = (error: unknown, fallback: string) => error instanceof Error ? error.message : fallback

export default function ConversationsPage({
  staff, initialConversationID, onSessionExpired, onSelectConversation,
}: {
  staff: StaffIdentity
  initialConversationID: number | null
  onSessionExpired: () => void
  onSelectConversation: (id: number | null) => void
}) {
  const [conversations, setConversations] = useState<StaffConversationSummary[]>([])
  const [selectedID, setSelectedID] = useState<number | null>(initialConversationID)
  const [messages, setMessages] = useState<StaffConversationMessage[]>([])
  const [draft, setDraft] = useState('')
  const [loading, setLoading] = useState(true)
  const [messagesLoading, setMessagesLoading] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [revision, setRevision] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError('')
    void listStaffConversations(controller.signal).then((items) => {
      if (controller.signal.aborted) return
      setConversations(items)
      setSelectedID((current) => {
        const requested = current ?? initialConversationID
        return requested && items.some((item) => item.id === requested) ? requested : items[0]?.id ?? null
      })
      setLoading(false)
    }).catch((reason: unknown) => {
      if (controller.signal.aborted) return
      const message = messageOf(reason, 'Не удалось загрузить диалоги.')
      setError(message)
      setLoading(false)
      if (message.includes('Сессия завершена')) onSessionExpired()
    })
    return () => controller.abort()
  }, [revision, initialConversationID, onSessionExpired])

  useEffect(() => {
    if (selectedID === null) {
      setMessages([])
      return
    }
    const controller = new AbortController()
    setMessagesLoading(true)
    void Promise.all([
      listStaffConversationMessages(selectedID, controller.signal),
      markStaffConversationRead(selectedID),
    ]).then(([items]) => {
      if (controller.signal.aborted) return
      setMessages([...items].reverse())
      setMessagesLoading(false)
      setConversations((current) => current.map((item) => item.id === selectedID ? { ...item, unread_count: 0 } : item))
    }).catch((reason: unknown) => {
      if (controller.signal.aborted) return
      const message = messageOf(reason, 'Не удалось открыть историю диалога.')
      setError(message)
      setMessagesLoading(false)
      if (message.includes('Сессия завершена')) onSessionExpired()
    })
    return () => controller.abort()
  }, [selectedID, revision, onSessionExpired])

  const selected = useMemo(() => conversations.find((item) => item.id === selectedID) ?? null, [conversations, selectedID])

  async function sendMessage(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!selected || !draft.trim() || busy) return
    setBusy(true)
    setError('')
    setNotice('')
    try {
      const message = await sendStaffConversationMessage(selected.id, draft)
      setMessages((current) => [...current, message])
      setDraft('')
      setNotice('Ответ сохранён и поставлен в очередь Telegram-доставки.')
      setRevision((value) => value + 1)
    } catch (reason: unknown) {
      const message = messageOf(reason, 'Не удалось отправить ответ.')
      setError(message)
      if (message.includes('Сессия завершена')) onSessionExpired()
    } finally {
      setBusy(false)
    }
  }

  function chooseConversation(id: number) {
    setSelectedID(id)
    onSelectConversation(id)
    setError('')
    setNotice('')
  }

  return <section className="section-page communications-page">
    <div className="welcome-row">
      <div><p className="eyebrow">СОПРОВОЖДЕНИЕ УЧЕНИКОВ</p><h1>Диалоги</h1>
        <p className="page-subtitle">Переписка с учениками, закреплёнными за вами. Отправка ответа создаёт задачу Telegram-доставки.</p>
      </div>
      <button className="secondary-button" type="button" disabled={loading} onClick={() => setRevision((value) => value + 1)}>Обновить</button>
    </div>
    {error && <p className="auth-inline-error" role="alert">{error}</p>}
    {notice && <p className="management-notice" role="status">{notice}</p>}
    <div className="conversation-layout">
      <aside className="panel conversation-list" aria-label="Ваши диалоги">
        <div className="panel-heading"><div><h2>Ученики</h2><p>{loading ? 'Загружаем…' : conversations.length + ' диалогов'}</p></div></div>
        {loading && <p className="catalog-message">Загружаем диалоги…</p>}
        {!loading && conversations.length === 0 && <p className="catalog-message">Пока нет активных диалогов с назначенными учениками.</p>}
        {conversations.map((item) => <button type="button" key={item.id}
          className={'conversation-list-item' + (item.id === selectedID ? ' conversation-list-item--active' : '')}
          onClick={() => chooseConversation(item.id)}>
          <span className="conversation-list-top"><strong>{item.student_display_name}</strong>{item.unread_count > 0 && <i>{item.unread_count}</i>}</span>
          <span className="conversation-preview">{item.last_message?.body_text || 'Нет сообщений'}</span>
          <time>{new Date(item.last_activity_at).toLocaleString('ru-RU')}</time>
        </button>)}
      </aside>
      <section className="panel conversation-thread" aria-label="Переписка">
        {selected && <>
          <div className="conversation-thread-heading"><div><h2>{selected.student_display_name}</h2><p>Ученик · диалог №{selected.id}</p></div></div>
          <div className="conversation-message-list">
            {messagesLoading && <p className="catalog-message">Загружаем сообщения…</p>}
            {!messagesLoading && messages.length === 0 && <p className="catalog-message">Начните переписку: отправьте ученику сообщение.</p>}
            {messages.map((message) => <article key={message.id} className={'conversation-message conversation-message--' + (message.sender_type === 'student' ? 'student' : 'curator')}>
              <span>{message.sender_type === 'student' ? 'Ученик' : staff.display_name}</span>
              <p>{message.body_text || 'Сообщение без текста'}</p>
              <time>{new Date(message.created_at).toLocaleString('ru-RU')}</time>
              {message.sender_type === 'curator' && message.delivery_status && <small>{message.delivery_status === 'sent' ? 'Отправлено в Telegram' : message.delivery_status === 'failed' ? 'Ошибка Telegram-доставки' : 'Ожидает Telegram-доставки'}</small>}
            </article>)}
          </div>
          <form className="conversation-compose" onSubmit={(event) => void sendMessage(event)}>
            <textarea value={draft} onChange={(event) => setDraft(event.target.value)} maxLength={4000} rows={3}
              placeholder="Напишите ответ ученику…" aria-label="Текст ответа" />
            <button className="primary-button" type="submit" disabled={busy || !draft.trim()}>{busy ? 'Сохраняем…' : 'Отправить ответ'}</button>
          </form>
        </>}
        {!selected && <div className="conversation-empty"><h2>Выберите диалог</h2><p>Здесь будет история переписки и форма ответа.</p></div>}
      </section>
    </div>
  </section>
}
