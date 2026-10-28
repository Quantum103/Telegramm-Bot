import { useEffect, useState, type FormEvent } from 'react'
import { createStaffAccount, listStaffAccounts, resetStaffPassword, setStaffAccountStatus, type StaffAccount, type StaffIdentity } from './api'
import './management.css'

const messageOf = (error: unknown, fallback: string) => error instanceof Error ? error.message : fallback

export default function StaffManagementPage({ staff, onSessionExpired }: { staff: StaffIdentity; onSessionExpired: () => void }) {
  const [accounts, setAccounts] = useState<StaffAccount[]>([])
  const [loading, setLoading] = useState(true)
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<'curator' | 'administrator'>('curator')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [createdPassword, setCreatedPassword] = useState('')
  const [passwordResetCandidate, setPasswordResetCandidate] = useState<StaffAccount | null>(null)
  const [passwordResetEmail, setPasswordResetEmail] = useState('')
  const [revision, setRevision] = useState(0)

  const resetEmailMatches = passwordResetCandidate !== null &&
    passwordResetEmail.trim().toLowerCase() === passwordResetCandidate.email.trim().toLowerCase()

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    void listStaffAccounts(controller.signal).then((value) => {
      if (controller.signal.aborted) return
      setAccounts(value)
      setLoading(false)
    }).catch((reason: unknown) => {
      if (controller.signal.aborted) return
      const message = messageOf(reason, 'Не удалось загрузить сотрудников.')
      setError(message)
      setLoading(false)
      if (message.includes('Сессия завершена')) onSessionExpired()
    })
    return () => controller.abort()
  }, [revision, onSessionExpired])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError('')
    setNotice('')
    setCreatedPassword('')
    setBusy(true)
    try {
      const created = await createStaffAccount(name, email, role)
      setCreatedPassword(created.temporary_password)
      setNotice(`Учётная запись ${created.account.email} создана. Передайте временный пароль сотруднику лично; он должен сменить его при первом входе.`)
      setName('')
      setEmail('')
      setRevision((value) => value + 1)
    } catch (reason: unknown) {
      const message = messageOf(reason, 'Не удалось создать учётную запись.')
      setError(message)
      if (message.includes('Сессия завершена')) onSessionExpired()
    } finally {
      setBusy(false)
    }
  }


  function requestPasswordReset(account: StaffAccount) {
    setError('')
    setNotice('')
    setCreatedPassword('')
    setPasswordResetEmail('')
    setPasswordResetCandidate(account)
  }

  function cancelPasswordReset() {
    if (busy) return
    setPasswordResetCandidate(null)
    setPasswordResetEmail('')
  }

  async function resetPassword() {
    const account = passwordResetCandidate
    if (!account || !resetEmailMatches || busy) return

    setError('')
    setCreatedPassword('')
    setBusy(true)
    try {
      const temporaryPassword = await resetStaffPassword(account.id)
      setPasswordResetCandidate(null)
      setPasswordResetEmail('')
      setCreatedPassword(temporaryPassword)
      setNotice(`Пароль для ${account.email} сброшен. Передайте новый временный пароль лично; при следующем входе его нужно сменить.`)
      setRevision((value) => value + 1)
    } catch (reason: unknown) {
      const message = messageOf(reason, 'Не удалось сбросить пароль.')
      setError(message)
      if (message.includes('Сессия завершена')) onSessionExpired()
    } finally {
      setBusy(false)
    }
  }

  async function toggleStatus(account: StaffAccount) {
    const nextStatus = account.account_status === 'active' ? 'inactive' : 'active'
    if (!window.confirm(nextStatus === 'inactive'
      ? `Отключить доступ для ${account.email}? Его текущие сессии будут отозваны.`
      : `Повторно активировать ${account.email}?`)) return
    setError('')
    setNotice('')
    setBusy(true)
    try {
      await setStaffAccountStatus(account.id, nextStatus)
      setNotice(nextStatus === 'inactive' ? `Доступ для ${account.email} отключён.` : `Доступ для ${account.email} восстановлен.`)
      setRevision((value) => value + 1)
    } catch (reason: unknown) {
      const message = messageOf(reason, 'Не удалось изменить статус сотрудника.')
      setError(message)
      if (message.includes('Сессия завершена')) onSessionExpired()
    } finally {
      setBusy(false)
    }
  }

  async function copyPassword() {
    try {
      await navigator.clipboard.writeText(createdPassword)
      setNotice('Временный пароль скопирован. Передайте его сотруднику безопасным способом.')
    } catch {
      setError('Автоматическое копирование не поддерживается. Выделите пароль и скопируйте вручную.')
    }
  }

  return <section className="section-page">
    <div className="welcome-row"><div><p className="eyebrow">ДОСТУП И ПОЛНОМОЧИЯ</p><h1>Сотрудники</h1><p className="page-subtitle">Создавайте администраторов и кураторов без email-рассылки.</p></div></div>
    {error && <p className="auth-inline-error" role="alert">{error}</p>}
    {notice && <p className="management-notice" role="status">{notice}</p>}
    {passwordResetCandidate && <div className="management-confirm-backdrop">
      <section className="panel management-confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="password-reset-title" aria-describedby="password-reset-description">
        <p className="eyebrow">ПРОВЕРКА ДЕЙСТВИЯ</p>
        <h2 id="password-reset-title">Сбросить пароль сотрудника?</h2>
        <p id="password-reset-description">Вы собираетесь сбросить пароль именно этой учётной записи. Это действие завершит все её активные сессии и потребует задать новый пароль при следующем входе.</p>
        <div className="management-confirm-identity">
          <span>Сотрудник</span>
          <strong>{passwordResetCandidate.display_name}</strong>
          <span>Email — введите его ниже для подтверждения</span>
          <code>{passwordResetCandidate.email}</code>
        </div>
        <form className="management-confirm-form" onSubmit={(event) => { event.preventDefault(); void resetPassword() }}>
          <label className="docx-field management-confirm-email">
            <span>Введите email этого сотрудника</span>
            <input
              className="docx-input"
              type="email"
              value={passwordResetEmail}
              onChange={(event) => setPasswordResetEmail(event.target.value)}
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              aria-describedby="password-reset-email-hint"
              placeholder={passwordResetCandidate.email}
              disabled={busy}
              required
              autoFocus
            />
          </label>
          <p id="password-reset-email-hint" className={passwordResetEmail && !resetEmailMatches ? 'management-confirm-hint is-mismatch' : 'management-confirm-hint'}>
            {resetEmailMatches ? 'Email совпадает. Сброс можно подтвердить.' : 'Адрес должен совпасть с email выбранного сотрудника.'}
          </p>
          <div className="management-confirm-actions">
            <button className="secondary-button" type="button" onClick={cancelPasswordReset} disabled={busy}>Отмена</button>
            <button className="primary-button management-danger-button" type="submit" disabled={!resetEmailMatches || busy}>
              {busy ? 'Сбрасываем…' : 'Да, сбросить пароль'}
            </button>
          </div>
        </form>
      </section>
    </div>}
    {createdPassword && <div className="panel management-password">
      <div><h2>Временный пароль</h2><p>Он показывается только в этом окне после создания. Скопируйте и передайте сотруднику лично. Не отправляйте его в общие чаты.</p></div>
      <code>{createdPassword}</code>
      <div className="management-actions"><button className="primary-button" type="button" onClick={() => void copyPassword()}>Скопировать пароль</button><button className="text-button" type="button" onClick={() => setCreatedPassword('')}>Скрыть пароль</button></div>
    </div>}
    <div className="panel management-create-panel">
      <div className="panel-heading"><div><h2>Новый сотрудник</h2><p>Пароль генерируется случайно, хранится в БД только в виде хеша и меняется при первом входе.</p></div></div>
      <form className="management-form" onSubmit={(event) => void submit(event)}>
        <label className="docx-field"><span>Имя сотрудника</span><input className="docx-input" value={name} onChange={(event) => setName(event.target.value)} maxLength={200} autoComplete="name" required /></label>
        <label className="docx-field"><span>Email для входа</span><input className="docx-input" type="email" value={email} onChange={(event) => setEmail(event.target.value)} maxLength={254} autoComplete="email" required /></label>
        <label className="docx-field"><span>Роль</span><select className="docx-input" value={role} onChange={(event) => setRole(event.target.value as 'curator' | 'administrator')}><option value="curator">Куратор</option><option value="administrator">Администратор</option></select></label>
        <button className="primary-button" type="submit" disabled={busy}>{busy ? 'Создаём…' : 'Создать сотрудника'}</button>
      </form>
    </div>
    <div className="panel table-panel management-staff-list">
      <div className="panel-heading"><div><h2>Учётные записи</h2><p>{loading ? 'Загружаем список…' : `${accounts.length} сотрудников`}</p></div><button className="secondary-button" type="button" onClick={() => setRevision((value) => value + 1)} disabled={loading}>Обновить</button></div>
      {loading && <p className="catalog-message" role="status">Загружаем сотрудников…</p>}
      {!loading && <div className="table-scroll"><table><thead><tr><th>Сотрудник</th><th>Роль</th><th>Статус</th><th>Первый вход</th><th>Создан</th><th>Действия</th></tr></thead><tbody>
        {accounts.map((account) => <tr key={account.id}><td><div className="management-person"><strong>{account.display_name}</strong><span>{account.email}</span></div></td><td>{account.roles.map((item) => item === 'administrator' ? 'Администратор' : 'Куратор').join(', ') || 'Без роли'}</td><td>{account.account_status}</td><td>{account.must_change_password ? 'Требуется смена' : 'Выполнено'}</td><td>{new Date(account.created_at).toLocaleDateString('ru-RU')}</td><td><div className="management-actions"><button className="text-button" type="button" disabled={busy || account.id === staff.id || account.account_status !== 'active'} onClick={() => requestPasswordReset(account)}>Сбросить пароль</button><button className="text-button" type="button" disabled={busy || account.id === staff.id} onClick={() => void toggleStatus(account)}>{account.account_status === 'active' ? 'Отключить' : 'Активировать'}</button></div></td></tr>)}
        {accounts.length === 0 && <tr><td colSpan={6} className="empty-state">Сотрудников пока нет.</td></tr>}
      </tbody></table></div>}
    </div>
  </section>
}
