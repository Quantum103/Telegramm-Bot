import { useEffect, useMemo, useState } from 'react'
import {
  assignStudentCurator, endStudentCuratorAssignment, listAdminStudents, listStaffAccounts,
  type AdminStudent, type StaffAccount, type StaffIdentity,
} from './api'
import './management.css'

const messageOf = (error: unknown, fallback: string) => error instanceof Error ? error.message : fallback

export default function StudentsPage({ staff, onSessionExpired }: { staff: StaffIdentity; onSessionExpired: () => void }) {
  const isAdmin = staff.roles.includes('administrator')
  const [students, setStudents] = useState<AdminStudent[]>([])
  const [curators, setCurators] = useState<StaffAccount[]>([])
  const [loading, setLoading] = useState(true)
  const [busyStudentID, setBusyStudentID] = useState<number | null>(null)
  const [selectedCurator, setSelectedCurator] = useState<Record<number, string>>({})
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [revision, setRevision] = useState(0)

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError('')
    const studentsRequest = listAdminStudents(controller.signal)
    const curatorsRequest = isAdmin
      ? listStaffAccounts(controller.signal).then((accounts) => {
        setCurators(accounts.filter((account) => account.account_status === 'active' && account.roles.includes('curator')))
      })
      : Promise.resolve()
    void Promise.all([studentsRequest, curatorsRequest]).then(([items]) => {
      if (controller.signal.aborted) return
      setStudents(items)
      setLoading(false)
    }).catch((reason: unknown) => {
      if (controller.signal.aborted) return
      const message = messageOf(reason, 'Не удалось загрузить учеников.')
      setError(message)
      setLoading(false)
      if (message.includes('Сессия завершена')) onSessionExpired()
    })
    return () => controller.abort()
  }, [isAdmin, revision, onSessionExpired])

  const sortedStudents = useMemo(
    () => [...students].sort((a, b) => a.display_name.localeCompare(b.display_name, 'ru')),
    [students],
  )

  async function assign(student: AdminStudent) {
    const value = selectedCurator[student.id] || (student.curator_staff_id ? String(student.curator_staff_id) : '')
    if (!value) {
      setError('Выберите куратора для ученика.')
      return
    }
    setBusyStudentID(student.id)
    setError('')
    setNotice('')
    try {
      await assignStudentCurator(student.id, Number(value))
      setNotice(`Назначение для «${student.display_name}» сохранено.`)
      setRevision((v) => v + 1)
    } catch (reason: unknown) {
      const message = messageOf(reason, 'Не удалось назначить куратора.')
      setError(message)
      if (message.includes('Сессия завершена')) onSessionExpired()
    } finally {
      setBusyStudentID(null)
    }
  }

  async function unassign(student: AdminStudent) {
    setBusyStudentID(student.id)
    setError('')
    setNotice('')
    try {
      await endStudentCuratorAssignment(student.id)
      setNotice(`Куратор снят с ученика «${student.display_name}».`)
      setRevision((v) => v + 1)
    } catch (reason: unknown) {
      const message = messageOf(reason, 'Не удалось снять куратора.')
      setError(message)
      if (message.includes('Сессия завершена')) onSessionExpired()
    } finally {
      setBusyStudentID(null)
    }
  }

  return <section className="section-page section-page--students">
    <div className="welcome-row">
      <div><p className="eyebrow">СОПРОВОЖДЕНИЕ УЧЕНИКОВ</p><h1>Ученики</h1>
        <p className="page-subtitle">{isAdmin ? 'Список учеников и их прогресс; назначайте и переводите кураторов.' : 'Здесь видны только ученики, назначенные вам.'}</p>
      </div>
      <button className="secondary-button" type="button" onClick={() => setRevision((v) => v + 1)} disabled={loading}>Обновить</button>
    </div>
    {error && <p className="auth-inline-error" role="alert">{error}</p>}
    {notice && <p className="management-notice" role="status">{notice}</p>}
    <div className="panel table-panel">
      <div className="panel-heading"><div><h2>Список учеников</h2><p>{loading ? 'Загружаем данные…' : `${students.length} учеников`}</p></div></div>
      {loading && <p className="catalog-message" role="status">Загружаем учеников и прогресс…</p>}
      {!loading && students.length === 0 && <p className="catalog-message">Учеников пока нет или вам ещё не назначили учеников.</p>}
      {!loading && students.length > 0 && <div className="table-scroll">
        <table><thead><tr><th>Ученик</th><th>Статус</th><th>Прогресс уроков</th><th>Последняя активность</th><th>Куратор</th>{isAdmin && <th>Управление</th>}</tr></thead>
          <tbody>{sortedStudents.map((student) => <tr key={student.id}>
            <td><div className="management-person"><strong>{student.preferred_name || student.display_name}</strong><span>{student.telegram_username ? '@' + student.telegram_username : 'Telegram ID: ' + student.telegram_id}</span></div></td>
            <td><span className={'course-state course-state--' + (student.account_status === 'active' ? 'published' : 'archived')}>{student.account_status}</span></td>
            <td>{student.completed_enrollments} из {student.total_enrollments} завершено</td>
            <td>{student.last_learning_activity_at ? new Date(student.last_learning_activity_at).toLocaleString('ru-RU') : 'Пока нет'}</td>
            <td>{student.curator_display_name || 'Не назначен'}</td>
            {isAdmin && <td><div className="management-actions">
              <select aria-label={'Куратор для ' + student.display_name} value={selectedCurator[student.id] ?? (student.curator_staff_id ? String(student.curator_staff_id) : '')}
                onChange={(event) => setSelectedCurator((current) => ({ ...current, [student.id]: event.target.value }))}>
                <option value="">Выберите куратора</option>
                {curators.map((curator) => <option key={curator.id} value={curator.id}>{curator.display_name} · {curator.email}</option>)}
              </select>
              <button className="secondary-button" type="button" disabled={busyStudentID === student.id || curators.length === 0} onClick={() => void assign(student)}>{busyStudentID === student.id ? 'Сохраняем…' : student.curator_staff_id ? 'Перевести' : 'Назначить'}</button>
              {student.curator_staff_id && <button className="text-button" type="button" disabled={busyStudentID === student.id} onClick={() => void unassign(student)}>Снять</button>}
            </div></td>}
          </tr>)}</tbody>
        </table>
      </div>}
    </div>
  </section>
}
