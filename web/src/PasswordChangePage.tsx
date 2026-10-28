import { useState, type FormEvent } from 'react'
import { changeStaffPassword, type StaffIdentity } from './api'
import './management.css'

export default function PasswordChangePage({ staff, onComplete }: { staff: StaffIdentity; onComplete: () => void }) {
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setError('')
    if (newPassword.length < 12) {
      setError('Новый пароль должен содержать не менее 12 символов.')
      return
    }
    if (newPassword.length > 1024) {
      setError('Новый пароль превышает допустимую длину.')
      return
    }
    if (newPassword !== confirmPassword) {
      setError('Новый пароль и подтверждение не совпадают.')
      return
    }
    setBusy(true)
    try {
      await changeStaffPassword(currentPassword, newPassword)
      onComplete()
    } catch (reason: unknown) {
      setError(reason instanceof Error ? reason.message : 'Не удалось сменить пароль.')
    } finally {
      setBusy(false)
    }
  }

  return <main className="auth-shell">
    <section className="auth-card" aria-labelledby="password-heading">
      <div className="auth-brand"><div className="auth-brand-mark" aria-hidden="true"><span /><span /><span /></div><div><strong>Слово</strong><span>ОБУЧАЮЩАЯ ПЛАТФОРМА</span></div></div>
      <p className="eyebrow">ЗАЩИТА УЧЁТНОЙ ЗАПИСИ</p><h1 id="password-heading">Создайте собственный пароль</h1>
      <p className="auth-subtitle">Учётная запись {staff.email} использует временный пароль. До его смены доступ к панели закрыт.</p>
      {error && <p className="auth-error" role="alert">{error}</p>}
      <form className="auth-form" onSubmit={(event) => void submit(event)}>
        <label className="docx-field"><span>Текущий временный пароль</span><input className="docx-input" type="password" autoComplete="current-password" value={currentPassword} onChange={(event) => setCurrentPassword(event.target.value)} maxLength={1024} required /></label>
        <label className="docx-field"><span>Новый пароль (минимум 12 символов)</span><input className="docx-input" type="password" autoComplete="new-password" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} minLength={12} maxLength={1024} required /></label>
        <label className="docx-field"><span>Повторите новый пароль</span><input className="docx-input" type="password" autoComplete="new-password" value={confirmPassword} onChange={(event) => setConfirmPassword(event.target.value)} minLength={12} maxLength={1024} required /></label>
        <button className="primary-button auth-submit" type="submit" disabled={busy}>{busy ? 'Сохраняем…' : 'Сменить пароль'}</button>
      </form>
      <p className="auth-footnote">После смены пароля все активные сессии этой учётной записи отзываются. Затем войдите с новым паролем.</p>
    </section>
  </main>
}
