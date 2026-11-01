import { useEffect, useState, type FormEvent } from 'react'
import {
  createStaffTelegramLinkToken, getStaffTelegramBindingStatus, getStaffNotificationPreferences,
  listCuratorTelegramNotifications, updateStaffNotificationPreferences,
  type CuratorNotification, type StaffIdentity, type StaffNotificationPreferences,
  type StaffTelegramBindingStatus, type StaffTelegramLinkToken,
} from './api'
import './communications.css'

const messageOf = (error: unknown, fallback: string) => error instanceof Error ? error.message : fallback

const deliveryState: Record<string, string> = {
  pending: 'Ожидает отправки',
  processing: 'Отправляется',
  retry: 'Повторная попытка запланирована',
  sent: 'Отправлено',
  failed: 'Не отправлено',
  cancelled: 'Отменено',
}

const deliveryError: Record<string, string> = {
  curator_telegram_not_linked: 'У назначенного куратора не привязан Telegram. Сообщение сохранено в диалоге; после привязки Telegram в разделе «Уведомления» доставка будет восстановлена автоматически.',
  telegram_recipient_unavailable: 'Бот не может написать в этот чат. Откройте бота и отправьте /start; проверьте, не заблокирован ли он.',
  curator_account_inactive: 'Учётная запись сотрудника отключена.',
  curator_role_inactive: 'У сотрудника больше нет активной роли куратора.',
  curator_assignment_missing: 'Ученик сейчас не закреплён за активным куратором.',
  telegram_rate_limited: 'Telegram временно ограничил частоту отправки. Система повторит попытку.',
  telegram_server_error: 'Временная ошибка Telegram. Система повторит попытку.',
  delivery_outcome_unknown: 'Исход сетевой отправки неизвестен. Система повторит попытку; возможен дубль.',
  admin_telegram_not_linked: 'Telegram ещё не привязан к активному администратору.',
  curator_authorization_check_failed: 'Не удалось проверить актуальное назначение; будет повторная попытка.',
  admin_authorization_check_failed: 'Не удалось проверить активного администратора; будет повторная попытка.',
  invalid_notification_timezone: 'В настройках указан некорректный часовой пояс.',
}

export default function TelegramSettingsPage({ staff, onSessionExpired }: { staff: StaffIdentity; onSessionExpired: () => void }) {
  const [status, setStatus] = useState<StaffTelegramBindingStatus | null>(null)
  const [items, setItems] = useState<CuratorNotification[]>([])
  const [token, setToken] = useState<StaffTelegramLinkToken | null>(null)
  const [confirmRebind, setConfirmRebind] = useState(false)
  const [preferences, setPreferences] = useState<StaffNotificationPreferences>({
    reminders_enabled: true, notifications_enabled: true, timezone: 'UTC',
    quiet_hours_enabled: false, quiet_start: '22:00', quiet_end: '08:00',
  })
  const [savingPreferences, setSavingPreferences] = useState(false)
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
      staff.roles.includes('curator')
        ? getStaffNotificationPreferences(controller.signal)
        : Promise.resolve(null),
    ]).then(([binding, notifications, prefs]) => {
      if (controller.signal.aborted) return
      setStatus(binding)
      setItems(notifications)
      if (prefs) setPreferences(prefs)
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


  async function savePreferences(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setSavingPreferences(true)
    setError('')
    setNotice('')
    try {
      await updateStaffNotificationPreferences(preferences)
      setNotice('Настройки уведомлений сохранены.')
      setRevision((value) => value + 1)
    } catch (reason: unknown) {
      const message = messageOf(reason, 'Не удалось сохранить настройки уведомлений.')
      setError(message)
      if (message.includes('Сессия завершена')) onSessionExpired()
    } finally {
      setSavingPreferences(false)
    }
  }

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
      <p className="page-subtitle">Привяжите личный Telegram-аккаунт. В журнале отображаются только непрочитанные входящие сообщения.</p>
      <p className="telegram-help-note">Уведомления для: {staff.display_name}</p>
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

    {staff.roles.includes('curator') && <form className="panel notification-preferences-panel" onSubmit={(event) => void savePreferences(event)}>
      <div><p className="eyebrow">ПЕРСОНАЛЬНЫЕ ПРАВИЛА</p><h2>Напоминания и тихие часы</h2>
        <p className="page-subtitle">Параметры влияют только на уведомления, отправляемые этому куратору. Сообщения учеников сохраняются независимо от этих настроек.</p>
      </div>
      <label className="notification-setting-row">
        <input type="checkbox" checked={preferences.reminders_enabled}
          onChange={(event) => setPreferences((current) => ({ ...current, reminders_enabled: event.target.checked }))} />
        <span><strong>Напоминания включены</strong><small>Первое через 4 часа, второе через 24 часа после последнего сообщения ученика.</small></span>
      </label>
      <label className="notification-setting-row">
        <input type="checkbox" checked={preferences.notifications_enabled}
          onChange={(event) => setPreferences((current) => ({ ...current, notifications_enabled: event.target.checked }))} />
        <span><strong>Обычные Telegram-уведомления включены</strong><small>При отключении доставка откладывается, а задачи остаются в PostgreSQL.</small></span>
      </label>
      <label className="docx-field notification-timezone-field"><span>Часовой пояс (IANA)</span>
        <input className="docx-input" value={preferences.timezone}
          onChange={(event) => setPreferences((current) => ({ ...current, timezone: event.target.value }))}
          placeholder="Europe/Amsterdam" maxLength={128} required />
        <small>Примеры: Europe/Amsterdam, Europe/Moscow, Asia/Almaty.</small>
      </label>
      <label className="notification-setting-row">
        <input type="checkbox" checked={preferences.quiet_hours_enabled}
          onChange={(event) => setPreferences((current) => ({ ...current, quiet_hours_enabled: event.target.checked }))} />
        <span><strong>Тихие часы включены</strong><small>Обычные уведомления и напоминания будут перенесены на окончание тихого периода.</small></span>
      </label>
      <div className="notification-time-grid">
        <label className="docx-field"><span>Начало тихих часов</span>
          <input className="docx-input" type="time" value={preferences.quiet_start}
            onChange={(event) => setPreferences((current) => ({ ...current, quiet_start: event.target.value }))} />
        </label>
        <label className="docx-field"><span>Окончание тихих часов</span>
          <input className="docx-input" type="time" value={preferences.quiet_end}
            onChange={(event) => setPreferences((current) => ({ ...current, quiet_end: event.target.value }))} />
        </label>
      </div>
      <p className="telegram-help-note">Тихие часы могут пересекать полночь. При включённых тихих часах одинаковое время начала и окончания не допускается.</p>
      <button className="primary-button" type="submit" disabled={savingPreferences}>{savingPreferences ? 'Сохраняем…' : 'Сохранить настройки'}</button>
    </form>}
    <section className="panel table-panel telegram-notifications-panel">
      <div className="panel-heading"><div><h2>Журнал уведомлений</h2><p>{loading ? 'Загружаем…' : items.length + ' непрочитанных сообщений'}</p></div></div>
      {loading && <p className="catalog-message">Загружаем историю отправки…</p>}
      {!loading && items.length === 0 && <p className="catalog-message">Непрочитанных сообщений нет. Новые записи появятся здесь, когда ученик напишет вам.</p>}
      {!loading && items.length > 0 && <div className="table-scroll"><table><thead><tr><th>Ученик</th><th>Тип</th><th>Состояние</th><th>Попытки</th><th>Последняя ошибка</th><th>Создано</th><th /></tr></thead><tbody>
        {items.map((item) => <tr key={item.id}><td><strong>{item.student_display_name}</strong></td>
          <td>{item.channel === 'admin_escalation' ? 'Эскалация' : item.channel === 'curator_transfer' ? 'Передача диалога' : item.channel === 'curator_reminder_4h' ? 'Напоминание 4 ч' : item.channel === 'curator_reminder_24h' ? 'Напоминание 24 ч' : 'Новое сообщение'}</td>
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
