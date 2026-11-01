import { useEffect, useState } from 'react'
import {
  createStaffTelegramLinkToken, getStaffTelegramBindingStatus, listCuratorTelegramNotifications,
  type CuratorNotification, type StaffIdentity, type StaffTelegramBindingStatus, type StaffTelegramLinkToken,
} from './api'
import './communications.css'

const messageOf = (error: unknown, fallback: string) => error instanceof Error ? error.message : fallback

const deliveryState: Record<string, string> = {
  pending: 'Ожидает отправки',
  processing: 'Отправляется',
  retry: 'Повторная попытка запланирована',
  sent: 'Отправлено',
  failed: 'Не отправлено',
}

const deliveryError: Record<string, string> = {
  curator_telegram_not_linked: 'Telegram ещё не привязан к учётной записи.',
  telegram_recipient_unavailable: 'Бот не может написать в этот чат. Откройте бота и отправьте /start; проверьте, не заблокирован ли он.',
  curator_account_inactive: 'Учётная запись сотрудника отключена.',
  curator_role_inactive: 'У сотрудника больше нет активной роли куратора.',
  curator_assignment_missing: 'Ученик сейчас не закреплён за активным куратором.',
  telegram_rate_limited: 'Telegram временно ограничил частоту отправки. Система повторит попытку.',
  telegram_server_error: 'Временная ошибка Telegram. Система повторит попытку.',
  delivery_outcome_unknown: 'Исход сетевой отправки неизвестен. Система повторит попытку; возможен дубль.',
}

export default function TelegramSettingsPage({ staff, onSessionExpired }: { staff: StaffIdentity; onSessionExpired: () => void }) {
  const [status, setStatus] = useState<StaffTelegramBindingStatus | null>(null)
  const [items, setItems] = useState<CuratorNotification[]>([])
  const [token, setToken] = useState<StaffTelegramLinkToken | null>(null)
  const [confirmRebind, setConfirmRebind] = useState(false)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [revision, setRevision] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    void Promise.all([
      getStaffTelegramBindingStatus(controller.signal),
      listCuratorTelegramNotifications(controller.signal),
    ]).then(([binding, notifications]) => {
      if (controller.signal.aborted) return
      setStatus(binding)
      setItems(notifications)
      setLoading(false)
    }).catch((reason: unknown) => {
      if (controller.signal.aborted) return
      const message = messageOf(reason, 'Не удалось загрузить состояние Telegram.')
      setError(message)
      setLoading(false)
      if (message.includes('Сессия завершена')) onSessionExpired()
    })
    return () => controller.abort()
  }, [revision, onSessionExpired])

  async function issueToken() {
    setBusy(true)
    setError('')
    setNotice('')
    try {
      const value = await createStaffTelegramLinkToken()
      setToken(value)
      setNotice('Код создан. Он действует 10 минут и может быть использован только один раз.')
    } catch (reason: unknown) {
      const message = messageOf(reason, 'Не удалось создать код привязки.')
      setError(message)
      if (message.includes('Сессия завершена')) onSessionExpired()
    } finally {
      setBusy(false)
    }
  }

  async function copyCommand() {
    if (!token) return
    try {
      await navigator.clipboard.writeText('/link ' + token.token)
      setNotice('Команда скопирована. Отправьте её боту в личном чате.')
    } catch {
      setError('Не удалось скопировать команду автоматически. Выделите её и скопируйте вручную.')
    }
  }

  function openDialog(conversationID: number) {
    const url = new URL(window.location.href)
    url.searchParams.set('page', 'dialogs')
    url.searchParams.set('conversation_id', String(conversationID))
    window.location.assign(url.toString())
  }

  return <section className="section-page communications-page">
    <div className="welcome-row"><div><p className="eyebrow">СВЯЗЬ С TELEGRAM</p><h1>Уведомления</h1>
      <p className="page-subtitle">Привяжите личный Telegram-аккаунт и отслеживайте состояние уведомлений о новых сообщениях учеников.</p>
    </div><button className="secondary-button" type="button" disabled={loading} onClick={() => setRevision((value) => value + 1)}>Обновить</button></div>
    {error && <p className="auth-inline-error" role="alert">{error}</p>}
    {notice && <p className="management-notice" role="status">{notice}</p>}
    <section className="panel telegram-binding-panel">
      <div><p className="eyebrow">ЛИЧНЫЙ TELEGRAM</p><h2>Привязка аккаунта</h2>
        <p>Используется Telegram ID отправителя команды, а не имя пользователя. Бот должен получить от вас личное сообщение, чтобы иметь возможность отправлять уведомления.</p>
      </div>
      <div className="telegram-binding-state">
        <span className={'course-state course-state--' + (status?.linked ? 'published' : 'archived')}>{status?.linked ? 'Telegram привязан' : 'Telegram не привязан'}</span>
        {status?.telegram_username && <span>@{status.telegram_username}</span>}
        {!status?.linked && <button className="primary-button" type="button" disabled={busy || loading} onClick={() => void issueToken()}>{busy ? 'Создаём код…' : 'Создать код привязки'}</button>}
        {status?.linked && !confirmRebind && <button className="secondary-button" type="button" disabled={busy || loading} onClick={() => setConfirmRebind(true)}>Сменить привязанный Telegram</button>}
        {status?.linked && confirmRebind && <div className="telegram-rebind-confirm">
          <p>После подтверждения уведомления будут приходить в новый Telegram-аккаунт. Текущая привязка будет заменена.</p>
          <button className="primary-button" type="button" disabled={busy} onClick={() => { setConfirmRebind(false); void issueToken() }}>Подтвердить замену</button>
          <button className="secondary-button" type="button" disabled={busy} onClick={() => setConfirmRebind(false)}>Отмена</button>
        </div>}
      </div>
    </section>
    {token && <section className="panel telegram-token-panel">
      <h2>Одноразовая команда</h2>
      <p>Откройте личный чат с ботом и отправьте эту команду. Код доступен только сейчас, действует до {new Date(token.expires_at).toLocaleTimeString('ru-RU')} и не сохраняется в открытом виде.</p>
      <code>/link {token.token}</code>
      <button className="secondary-button" type="button" onClick={() => void copyCommand()}>Скопировать команду</button>
    </section>}
    <section className="panel table-panel telegram-notifications-panel">
      <div className="panel-heading"><div><h2>Журнал уведомлений</h2><p>{loading ? 'Загружаем…' : items.length + ' последних задач'}</p></div></div>
      {loading && <p className="catalog-message">Загружаем историю отправки…</p>}
      {!loading && items.length === 0 && <p className="catalog-message">Пока нет уведомлений. Когда ученик напишет вам, запись появится здесь.</p>}
      {!loading && items.length > 0 && <div className="table-scroll"><table><thead><tr><th>Ученик</th><th>Состояние</th><th>Попытки</th><th>Последняя ошибка</th><th>Создано</th><th /></tr></thead><tbody>
        {items.map((item) => <tr key={item.id}><td><strong>{item.student_display_name}</strong></td>
          <td><span className={'course-state course-state--' + (item.delivery_status === 'sent' ? 'published' : item.delivery_status === 'failed' ? 'archived' : 'draft')}>{deliveryState[item.delivery_status] || item.delivery_status}</span></td>
          <td>{item.attempt_count}</td>
          <td>{item.last_error_code ? (deliveryError[item.last_error_code] || item.last_error_code) : '—'}</td>
          <td>{new Date(item.created_at).toLocaleString('ru-RU')}</td>
          <td><button className="row-action" type="button" aria-label="Открыть диалог" onClick={() => openDialog(item.conversation_id)}>Открыть</button></td>
        </tr>)}
      </tbody></table></div>}
    </section>
    <p className="telegram-help-note">Если бот заблокирован или вы ещё не запускали его, откройте чат с ботом, нажмите «Start» или отправьте /start. После повторного запуска система попробует восстановить уведомления, для которых ошибка была временной или связанной с доступом к чату.</p>
  </section>
}
