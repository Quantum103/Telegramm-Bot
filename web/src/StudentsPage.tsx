import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import {
  assignStudentCurator, endStudentCuratorAssignment, getCourseLessonDetail,
  listAdminStudents, listCuratorCourseLessons, listCuratorLessonAnswers, listStaffAccounts,
  listStaffConversations, reviewCuratorAnswerAttempt,
  type AdminStudent, type AnswerAttempt, type CourseEnrollmentLesson, type CourseLessonDetail,
  type CuratorAnswer, type CuratorCourseAccess, type StaffAccount, type StaffConversationSummary,
  type StaffIdentity,
} from './api'
import './management.css'

const messageOf = (error: unknown, fallback: string) => error instanceof Error ? error.message : fallback
const dateText = (value?: string) => value ? new Date(value).toLocaleString('ru-RU') : 'Пока нет активности'

function reviewLabel(decision?: string, status?: string, attempts = 1): string {
  if (decision === 'approved') return 'Принят'
  if (decision === 'needs_revision') return 'Требуется исправление'
  if (status === 'pending_review') return attempts > 1 ? 'Повторно отправлен' : 'Ожидает проверки'
  if (status === 'not_submitted') return 'Не отправлен'
  if (status === 'pending') return 'Обрабатывается'
  return 'Без проверки'
}

function reviewClass(decision?: string, status?: string): string {
  if (decision === 'approved') return 'approved'
  if (decision === 'needs_revision') return 'revision'
  if (status === 'pending_review') return 'pending'
  return 'neutral'
}

function lessonLabel(lesson: CourseEnrollmentLesson): string {
  if (lesson.status === 'completed') return 'Завершён'
  if (lesson.pending_reviews > 0) return 'Ожидает проверки'
  if (lesson.needs_revision > 0) return 'Требуется исправление'
  if (lesson.answered_questions > 0 || lesson.status === 'in_progress') return 'В процессе'
  if (lesson.status === 'awaiting_curator') return 'Ожидает куратора'
  return 'Не начат'
}

function latestAttempt(answer: CuratorAnswer): AnswerAttempt | undefined {
  return answer.attempts[0]
}

export default function StudentsPage({
  staff, onSessionExpired, onOpenConversation,
}: {
  staff: StaffIdentity
  onSessionExpired: () => void
  onOpenConversation: (conversationID: number, initialDraft: string) => void
}) {
  const isAdmin = staff.roles.includes('administrator')
  const isCurator = staff.roles.includes('curator')
  const [students, setStudents] = useState<AdminStudent[]>([])
  const [curators, setCurators] = useState<StaffAccount[]>([])
  const [conversations, setConversations] = useState<StaffConversationSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [busyStudentID, setBusyStudentID] = useState<number | null>(null)
  const [selectedCurator, setSelectedCurator] = useState<Record<number, string>>({})
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [revision, setRevision] = useState(0)
  const [selectedStudentID, setSelectedStudentID] = useState<number | null>(null)
  const [courseAccess, setCourseAccess] = useState<CuratorCourseAccess | null>(null)
  const [courseLoading, setCourseLoading] = useState(false)
  const [detailRevision, setDetailRevision] = useState(0)
  const [expandedLessonIDs, setExpandedLessonIDs] = useState<number[]>([])
  const [answersByLesson, setAnswersByLesson] = useState<Record<number, CuratorAnswer[]>>({})
  const [lessonDetailsByLesson, setLessonDetailsByLesson] = useState<Record<number, CourseLessonDetail>>({})
  const [answersLoaded, setAnswersLoaded] = useState<Record<number, boolean>>({})
  const [answersLoading, setAnswersLoading] = useState<Record<number, boolean>>({})
  const [answersErrors, setAnswersErrors] = useState<Record<number, string>>({})
  const [revisionQuestionID, setRevisionQuestionID] = useState<number | null>(null)
  const [feedbackDrafts, setFeedbackDrafts] = useState<Record<number, string>>({})
  const [busyAttemptID, setBusyAttemptID] = useState<number | null>(null)
  const busyAttemptRef = useRef<number | null>(null)
  const answerLoadingRef = useRef(new Set<number>())

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError('')
    const conversationsRequest = isCurator
      ? listStaffConversations(controller.signal)
      : Promise.resolve<StaffConversationSummary[]>([])
    const curatorsRequest = isAdmin
      ? listStaffAccounts(controller.signal).then((accounts) =>
        accounts.filter((account) => account.account_status === 'active' && account.roles.includes('curator')))
      : Promise.resolve<StaffAccount[]>([])
    void Promise.all([
      listAdminStudents(controller.signal), conversationsRequest, curatorsRequest,
    ]).then(([items, dialogs, accounts]) => {
      if (controller.signal.aborted) return
      setStudents(items)
      setConversations(dialogs)
      setCurators(accounts)
      setLoading(false)
    }).catch((reason: unknown) => {
      if (controller.signal.aborted) return
      const message = messageOf(reason, 'Не удалось загрузить учеников.')
      setError(message)
      setLoading(false)
      if (message.includes('Сессия завершена')) onSessionExpired()
    })
    return () => controller.abort()
  }, [isAdmin, isCurator, revision, onSessionExpired])

  const sortedStudents = useMemo(
    () => [...students].sort((a, b) => (a.preferred_name || a.display_name).localeCompare(b.preferred_name || b.display_name, 'ru')),
    [students],
  )
  const selectedStudent = useMemo(
    () => students.find((student) => student.id === selectedStudentID) ?? null,
    [students, selectedStudentID],
  )
  const activeCourse = selectedStudent?.active_course ?? null
  const selectedConversation = selectedStudent
    ? conversations.find((conversation) => conversation.student_id === selectedStudent.id) ?? null
    : null

  useEffect(() => {
    if (selectedStudentID !== null && !loading && !students.some((student) => student.id === selectedStudentID)) {
      setSelectedStudentID(null)
      setCourseAccess(null)
    }
  }, [loading, selectedStudentID, students])

  useEffect(() => {
    if (!selectedStudent || !selectedStudent.active_course) {
      setCourseAccess(null)
      setCourseLoading(false)
      return
    }
    const controller = new AbortController()
    setCourseLoading(true)
    setCourseAccess(null)
    setExpandedLessonIDs([])
    setAnswersByLesson({})
    setLessonDetailsByLesson({})
    setAnswersLoaded({})
    setAnswersLoading({})
    setAnswersErrors({})
    answerLoadingRef.current.clear()
    void listCuratorCourseLessons(
      selectedStudent.id, selectedStudent.active_course.course_enrollment_id, controller.signal,
    ).then((result) => {
      if (controller.signal.aborted) return
      setCourseAccess(result)
      setCourseLoading(false)
    }).catch((reason: unknown) => {
      if (controller.signal.aborted) return
      setError(messageOf(reason, 'Не удалось загрузить уроки курса ученика.'))
      setCourseLoading(false)
      if (messageOf(reason, '').includes('Сессия завершена')) onSessionExpired()
      if (messageOf(reason, '').includes('не найден')) {
        setSelectedStudentID(null)
      }
    })
    return () => controller.abort()
  }, [selectedStudent?.id, selectedStudent?.active_course?.course_enrollment_id, detailRevision, onSessionExpired])

  async function loadLessonAnswers(lesson: CourseEnrollmentLesson, force = false) {
    if (!selectedStudent || !activeCourse) return
    if (!force && (answersLoaded[lesson.lesson_id] || answerLoadingRef.current.has(lesson.lesson_id))) return
    answerLoadingRef.current.add(lesson.lesson_id)
    setAnswersLoading((current) => ({ ...current, [lesson.lesson_id]: true }))
    setAnswersErrors((current) => ({ ...current, [lesson.lesson_id]: '' }))
    try {
      const [answers, lessonDetail] = await Promise.all([
        listCuratorLessonAnswers(selectedStudent.id, activeCourse.course_enrollment_id, lesson.lesson_id),
        getCourseLessonDetail(activeCourse.course_id, lesson.lesson_id),
      ])
      setAnswersByLesson((current) => ({ ...current, [lesson.lesson_id]: answers }))
      setLessonDetailsByLesson((current) => ({ ...current, [lesson.lesson_id]: lessonDetail }))
      setAnswersLoaded((current) => ({ ...current, [lesson.lesson_id]: true }))
    } catch (reason: unknown) {
      const message = messageOf(reason, 'Не удалось загрузить ответы урока.')
      setAnswersErrors((current) => ({ ...current, [lesson.lesson_id]: message }))
      if (message.includes('Сессия завершена')) onSessionExpired()
    } finally {
      answerLoadingRef.current.delete(lesson.lesson_id)
      setAnswersLoading((current) => ({ ...current, [lesson.lesson_id]: false }))
    }
  }

  function toggleLesson(lesson: CourseEnrollmentLesson) {
    const isExpanded = expandedLessonIDs.includes(lesson.lesson_id)
    setExpandedLessonIDs((current) => isExpanded
      ? current.filter((id) => id !== lesson.lesson_id)
      : [...current, lesson.lesson_id])
    if (!isExpanded) void loadLessonAnswers(lesson)
  }

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

  function openChat(student: AdminStudent, lesson?: CourseEnrollmentLesson, answer?: CuratorAnswer) {
    if (!isCurator) return
    const conversation = conversations.find((item) => item.student_id === student.id)
    if (!conversation) {
      setNotice('У ученика пока нет активного диалога. Новый чат здесь не создаётся.')
      return
    }
    const lines = [
      'Здравствуйте!',
      '',
      `Курс: ${student.active_course?.course_title ?? 'не указан'}`,
      ...(lesson ? [`Урок: ${lesson.title}`] : []),
      ...(answer ? [`Вопрос: ${answer.prompt}`, `Ответ ученика: ${answer.submitted_answer || 'выбран вариант ответа'}`] : []),
      ...(answer?.review_feedback ? [`Предыдущее замечание: ${answer.review_feedback}`] : []),
      '',
      'Давайте обсудим это в чате.',
    ]
    onOpenConversation(conversation.id, lines.join('\n'))
  }

  async function submitReview(
    lesson: CourseEnrollmentLesson, answer: CuratorAnswer, decision: 'approved' | 'needs_revision',
  ) {
    const attemptID = answer.attempt_id
    if (!attemptID || busyAttemptRef.current === attemptID) return
    const feedback = feedbackDrafts[answer.question_id] ?? ''
    if (decision === 'needs_revision' && !feedback.trim()) {
      setAnswersErrors((current) => ({ ...current, [lesson.lesson_id]: 'Напишите, что ученику нужно исправить.' }))
      return
    }
    busyAttemptRef.current = attemptID
    setBusyAttemptID(attemptID)
    setAnswersErrors((current) => ({ ...current, [lesson.lesson_id]: '' }))
    setNotice('')
    try {
      const result = await reviewCuratorAnswerAttempt(attemptID, decision, decision === 'approved' ? '' : feedback)
      setNotice(result.replayed ? 'Решение уже было сохранено ранее.' : decision === 'approved'
        ? 'Ответ принят.' : 'Ответ отправлен ученику на исправление.')
      setRevision((value) => value + 1)
      setDetailRevision((value) => value + 1)
      setRevisionQuestionID(null)
      setFeedbackDrafts((current) => ({ ...current, [answer.question_id]: '' }))
    } catch (reason: unknown) {
      const message = messageOf(reason, 'Не удалось сохранить решение.')
      setAnswersErrors((current) => ({ ...current, [lesson.lesson_id]: message }))
      if (message.includes('Сессия завершена')) onSessionExpired()
    } finally {
      busyAttemptRef.current = null
      setBusyAttemptID(null)
    }
  }

  function chatButton(student: AdminStudent, lesson?: CourseEnrollmentLesson, answer?: CuratorAnswer) {
    if (!isCurator) return null
    const hasConversation = conversations.some((item) => item.student_id === student.id)
    return <button className="text-button" type="button" disabled={!hasConversation}
      title={hasConversation ? 'Открыть существующий диалог с учеником' : 'У ученика пока нет активного диалога'}
      onClick={() => openChat(student, lesson, answer)}>Написать ученику</button>
  }

  return <section className="section-page section-page--students">
    <div className="welcome-row">
      <div><p className="eyebrow">СОПРОВОЖДЕНИЕ УЧЕНИКОВ</p><h1>Ученики</h1>
        <p className="page-subtitle">{isAdmin ? 'Прогресс всех учеников, ответы и распределение кураторов.' : 'Ваш список учеников, их обучение и ответы на проверку.'}</p>
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
        <table className="students-table"><thead><tr>
          <th>Ученик</th><th>Активный курс</th><th>Прогресс</th><th>Ожидают проверки</th><th>Требуют исправления</th><th>Последняя активность</th>
          {isAdmin && <th>Куратор</th>}<th>Карточка</th>{isAdmin && <th>Управление</th>}
        </tr></thead><tbody>{sortedStudents.map((student) => {
          const course = student.active_course
          const progress = course && course.total_lessons > 0
            ? Math.round(course.completed_lessons / course.total_lessons * 100) : 0
          const lastActivity = student.last_learning_activity_at || student.last_active_at
          return <tr key={student.id} className={selectedStudentID === student.id ? 'students-table-row--selected' : ''}>
            <td><div className="management-person"><strong>{student.preferred_name || student.display_name}</strong><span>{student.telegram_username ? '@' + student.telegram_username : 'Telegram ID: ' + student.telegram_id}</span></div></td>
            <td>{course?.course_title || 'Нет активного курса'}</td>
            <td><div className="students-progress-cell"><strong>{course ? `${course.completed_lessons} из ${course.total_lessons}` : '—'}</strong>
              {course && <div className="students-progress-track" role="progressbar" aria-label="Прогресс курса" aria-valuemin={0} aria-valuemax={100} aria-valuenow={progress}><span style={{ width: progress + '%' }} /></div>}
            </div></td>
            <td><span className={course?.pending_reviews ? 'students-count students-count--pending' : 'students-count'}>{course?.pending_reviews ?? 0}</span></td>
            <td><span className={course?.needs_revision ? 'students-count students-count--revision' : 'students-count'}>{course?.needs_revision ?? 0}</span></td>
            <td>{lastActivity ? dateText(lastActivity) : 'Пока нет'}</td>
            {isAdmin && <td>{student.curator_display_name || 'Не назначен'}</td>}
            <td><button className="secondary-button students-open-button" type="button"
              onClick={() => { setSelectedStudentID(student.id); setNotice(''); setError('') }}>
              {selectedStudentID === student.id ? 'Открыта' : 'Открыть'}
            </button></td>
            {isAdmin && <td><div className="management-actions">
              <select aria-label={'Куратор для ' + student.display_name}
                value={selectedCurator[student.id] ?? (student.curator_staff_id ? String(student.curator_staff_id) : '')}
                onChange={(event) => setSelectedCurator((current) => ({ ...current, [student.id]: event.target.value }))}>
                <option value="">Выберите куратора</option>
                {curators.map((curator) => <option key={curator.id} value={curator.id}>{curator.display_name} · {curator.email}</option>)}
              </select>
              <button className="secondary-button" type="button" disabled={busyStudentID === student.id || curators.length === 0}
                onClick={() => void assign(student)}>{busyStudentID === student.id ? 'Сохраняем…' : student.curator_staff_id ? 'Перевести' : 'Назначить'}</button>
              {student.curator_staff_id && <button className="text-button" type="button" disabled={busyStudentID === student.id} onClick={() => void unassign(student)}>Снять</button>}
            </div></td>}
          </tr>
        })}</tbody></table>
      </div>}
    </div>

    {selectedStudent && <section className="panel student-detail-panel" aria-labelledby="student-detail-title">
      <div className="student-detail-heading">
        <div><p className="eyebrow">КАРТОЧКА УЧЕНИКА</p><h2 id="student-detail-title">{selectedStudent.preferred_name || selectedStudent.display_name}</h2>
          <p>{activeCourse?.course_title || 'Нет активного курса'}{activeCourse ? ` · ${activeCourse.mode === 'curator' ? 'С куратором' : 'С ИИ'}` : ''}</p>
        </div>
        <div className="student-detail-actions">
          {chatButton(selectedStudent)}
          <button className="icon-button" type="button" aria-label="Закрыть карточку ученика" onClick={() => { setSelectedStudentID(null); setCourseAccess(null) }}>×</button>
        </div>
      </div>
      {activeCourse && <div className="student-course-summary">
        <div><span>Прогресс курса</span><strong>{activeCourse.completed_lessons} / {activeCourse.total_lessons} уроков</strong></div>
        <div><span>Ожидают проверки</span><strong>{activeCourse.pending_reviews}</strong></div>
        <div><span>Требуют исправления</span><strong>{activeCourse.needs_revision}</strong></div>
        <div><span>Последняя активность</span><strong>{dateText(selectedStudent.last_learning_activity_at || selectedStudent.last_active_at)}</strong></div>
      </div>}
      {courseLoading && <p className="catalog-message" role="status">Загружаем уроки курса…</p>}
      {!courseLoading && !activeCourse && <p className="catalog-message">У ученика сейчас нет активного курса.</p>}
      {!courseLoading && activeCourse && courseAccess && <div className="student-lessons">
        <div className="student-lessons-heading"><h3>Уроки курса</h3><span>{courseAccess.lessons.length} уроков</span></div>
        {courseAccess.lessons.map((lesson) => {
          const expanded = expandedLessonIDs.includes(lesson.lesson_id)
          const answers = answersByLesson[lesson.lesson_id] ?? []
          const questionOptions = lessonDetailsByLesson[lesson.lesson_id]?.questions ?? []
          return <article className="student-lesson-card" key={lesson.lesson_id}>
            <button className="student-lesson-toggle" type="button" aria-expanded={expanded} onClick={() => toggleLesson(lesson)}>
              <span className="student-lesson-number">{lesson.position}</span>
              <span className="student-lesson-title"><strong>{lesson.title}</strong><small>{lesson.answered_questions} из {lesson.total_questions} ответов</small></span>
              <span className={'review-state review-state--' + (
                lesson.status === 'completed' ? 'approved' :
                  lesson.pending_reviews > 0 ? 'pending' :
                    lesson.needs_revision > 0 ? 'revision' : 'neutral'
              )}>{lessonLabel(lesson)}</span>
              <span className="student-lesson-chevron" aria-hidden="true">{expanded ? '−' : '+'}</span>
            </button>
            {expanded && <div className="student-lesson-content">
              {answersLoading[lesson.lesson_id] && <p className="catalog-message" role="status">Загружаем ответы и историю попыток…</p>}
              {answersErrors[lesson.lesson_id] && <div className="catalog-error" role="alert"><span>{answersErrors[lesson.lesson_id]}</span>
                <button className="secondary-button" type="button" onClick={() => void loadLessonAnswers(lesson, true)}>Повторить</button></div>}
              {!answersLoading[lesson.lesson_id] && answersLoaded[lesson.lesson_id] && answers.map((answer) => {
                const latest = latestAttempt(answer)
                const options = questionOptions.find((question) => question.id === answer.question_id)?.options ?? []
                const selectedOptions = options.filter((option) => answer.selected_option_ids.includes(option.id))
                const canReview = isCurator && activeCourse.mode === 'curator' &&
                  answer.attempt_id !== undefined && answer.review_status === 'pending_review' &&
                  answer.processing_status === 'pending'
                const isBusy = answer.attempt_id !== undefined && busyAttemptID === answer.attempt_id
                return <article className="curator-answer-card" key={answer.question_id}>
                  <div className="curator-answer-heading"><strong>Вопрос {answer.question_number}</strong>
                    <span className={'review-state review-state--' + reviewClass(answer.review_decision, answer.review_status)}>
                      {reviewLabel(answer.review_decision, answer.review_status, answer.attempts.length)}
                    </span>
                  </div>
                  <p className="curator-question-text">{answer.prompt}</p>
                  <div className="curator-submitted-answer">
                    <span>Ответ ученика</span>
                    <p>{answer.submitted_answer || (selectedOptions.length ? selectedOptions.map((option) => option.text).join('\n') : 'Ответ ещё не отправлен.')}</p>
                    {answer.submitted_at && <time>Отправлен: {dateText(answer.submitted_at)}</time>}
                  </div>
                  {answer.review_feedback && <p className="curator-review-feedback"><strong>Комментарий куратора:</strong> {answer.review_feedback}
                    {answer.reviewer_display_name ? ` · ${answer.reviewer_display_name}` : ''}{answer.reviewed_at ? ` · ${dateText(answer.reviewed_at)}` : ''}
                  </p>}
                  {answer.attempts.length > 0 && <details className="answer-history">
                    <summary>История попыток · {answer.attempts.length}</summary>
                    <div className="answer-history-list">{answer.attempts.map((attempt) => {
                      const attemptOptions = options.filter((option) => attempt.selected_option_ids.includes(option.id))
                      return <div className="answer-history-item" key={attempt.id}>
                        <div><strong>Попытка #{attempt.id}</strong><span className={'review-state review-state--' + reviewClass(attempt.review_decision, attempt.review_decision ? attempt.review_decision : attempt.processing_status === 'pending' ? 'pending_review' : '')}>{reviewLabel(attempt.review_decision, attempt.processing_status === 'pending' && !attempt.review_decision ? 'pending_review' : attempt.processing_status, answer.attempts.length)}</span></div>
                        <p>{attempt.submitted_answer || (attemptOptions.length ? attemptOptions.map((option) => option.text).join('\n') : 'Текстовый ответ не сохранён.')}</p>
                        <time>Отправлен: {dateText(attempt.created_at)}</time>
                        {attempt.reviewed_at && <time>Проверен: {dateText(attempt.reviewed_at)}{attempt.reviewer_display_name ? ` · ${attempt.reviewer_display_name}` : ''}</time>}
                        {attempt.review_feedback && <p className="answer-history-feedback">{attempt.review_feedback}</p>}
                      </div>
                    })}</div>
                  </details>}
                  <div className="curator-answer-actions">
                    {chatButton(selectedStudent, lesson, answer)}
                    {canReview && <>
                      <button className="primary-button" type="button" disabled={busyAttemptID !== null}
                        onClick={() => void submitReview(lesson, answer, 'approved')}>{isBusy ? 'Сохраняем…' : 'Принять ответ'}</button>
                      <button className="secondary-button" type="button" disabled={busyAttemptID !== null}
                        onClick={() => setRevisionQuestionID((current) => current === answer.question_id ? null : answer.question_id)}>Попросить исправить</button>
                    </>}
                  </div>
                  {canReview && revisionQuestionID === answer.question_id && <div className="review-feedback-form">
                    <label htmlFor={`review-feedback-${answer.question_id}`}>Объяснение для ученика</label>
                    <textarea id={`review-feedback-${answer.question_id}`} rows={3} maxLength={4000}
                      value={feedbackDrafts[answer.question_id] ?? ''}
                      onChange={(event) => setFeedbackDrafts((current) => ({ ...current, [answer.question_id]: event.target.value }))}
                      placeholder="Укажите, что нужно уточнить или исправить…" />
                    <div className="review-feedback-footer"><span>{(feedbackDrafts[answer.question_id] ?? '').length}/4000</span>
                      <button className="primary-button" type="button" disabled={busyAttemptID !== null || !(feedbackDrafts[answer.question_id] ?? '').trim()}
                        onClick={() => void submitReview(lesson, answer, 'needs_revision')}>{isBusy ? 'Сохраняем…' : 'Отправить на исправление'}</button>
                    </div>
                  </div>}
                  {latest && latest.processing_status !== 'pending' && !latest.review_decision && <p className="answer-processing-note">Результат обработки: {latest.processing_status}</p>}
                </article>
              })}
              {!answersLoading[lesson.lesson_id] && answersLoaded[lesson.lesson_id] && answers.length === 0 &&
                <p className="catalog-message">В этом уроке пока нет вопросов или ответов для просмотра.</p>}
            </div>}
          </article>
        })}
      </div>}
    </section>}
  </section>
}
