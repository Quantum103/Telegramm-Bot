import { useCallback, useEffect, useState } from 'react'
import './miniapp.css'

type TelegramWebApp = {
  initData: string
  ready: () => void
  expand: () => void
  setHeaderColor?: (color: string) => void
  setBackgroundColor?: (color: string) => void
  disableVerticalSwipes?: () => void
}

declare global {
  interface Window {
    Telegram?: { WebApp?: TelegramWebApp }
  }
}

type Course = {
  id: number
  slug: string
  title: string
  description?: string | null
  status: string
  lesson_count: number
}

type CabinetCourse = {
  course_id: number
  title: string
  mode: string
  status: string
  total_lessons: number
  completed_lessons: number
  pending_reviews: number
  needs_revision_answers: number
}

type Cabinet = {
  registered_at: string
  completed_courses: number
  completed_lessons: number
  unread_conversation_messages: number
  courses: CabinetCourse[]
}

type CourseRun = {
  enrollment: {
    id: number
    course_id: number
    course_title: string
    mode: 'ai' | 'curator'
    status: string
  }
  total_lessons: number
  completed_lessons: number
  remaining_lessons: number
  is_complete: boolean
}

type CourseLesson = {
  id: number
  title: string
  description?: string | null
  question_count: number
}

type RunLesson = {
  lesson_id: number
  lesson_version_id: number
  position: number
  title: string
  enrollment_id?: number
  status: string
  total_questions: number
  answered_questions: number
  pending_reviews: number
  needs_revision: number
  needs_revision_answers: number
}

type Progress = {
  enrollment: {
    id: number
    lesson_id: number
    status: string
    current_question_id?: number | null
  }
  answered_questions: number
  total_questions: number
  remaining_questions: number
  is_complete: boolean
  first_question_id?: number | null
  previous_question_id?: number | null
  next_question_id?: number | null
}

type Question = {
  id: number
  number: number
  prompt: string
  answer_type: 'text' | 'single_choice' | 'multiple_choice' | string
  options: { id: number; key: string; text: string }[]
  bible_passages: {
    book_name: string
    chapter: number
    start_verse: number
    end_verse: number
    passage_text?: string | null
    translation_name?: string | null
  }[]
  saved_answer?: {
    answer_text: string
    selected_option_ids: number[]
    processing_status: string
  }
  latest_attempt?: {
    processing_status: string
    review_decision?: string | null
    review_feedback?: string | null
  }
}

type QuestionState = {
  question: Question | null
  progress: Progress
  at_end: boolean
}

type LessonQuestionsState = {
  questions: Question[]
  progress: Progress
  at_end: boolean
}

type AnswerDraft = {
  answerText: string
  selectedOptionIDs: number[]
  dirty: boolean
}

type Bootstrap = {
  student: { display_name: string }
  courses: Course[]
  cabinet: Cabinet
  active_course?: CourseRun
}

type CourseDetails = {
  course: Course
  lessons: CourseLesson[]
  active_course?: CourseRun
}

type View = 'account' | 'catalog' | 'course' | 'mode' | 'lessons' | 'lesson'

const errorLabels: Record<string, string> = {
  mini_app_unavailable: 'Приложение временно недоступно. Попробуйте позже.',
  telegram_auth_invalid: 'Не удалось подтвердить ваш вход через Telegram. Закройте приложение и откройте его снова.',
  no_curator_available: 'Сейчас нет доступного координатора с подключённым Telegram. Курс не начат. Можно выбрать самостоятельное прохождение или повторить попытку позже.',
  active_course_exists: 'У вас уже есть незавершённый курс. Сначала продолжите его.',
  legacy_progress: 'Найдено старое прохождение, которое нужно восстановить через администратора.',
  invalid_state: 'Это действие сейчас недоступно. Обновите экран и попробуйте снова.',
  invalid_request: 'Проверьте ответ и попробуйте ещё раз.',
  not_found: 'Запрошенный материал больше недоступен.',
  internal_error: 'Не удалось выполнить действие. Попробуйте ещё раз.',
}

async function requestJSON<T>(initData: string, path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')
  headers.set('X-Telegram-Init-Data', initData)
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')

  const response = await fetch(path, {
    ...init,
    headers,
    credentials: 'same-origin',
    cache: 'no-store',
  })
  const body = await response.json().catch(() => null) as (T & { error?: string }) | null
  if (!response.ok) {
    const code = body && typeof body.error === 'string' ? body.error : ''
    throw new Error(errorLabels[code] || 'Не удалось связаться с сервером. Проверьте подключение.')
  }
  if (body === null) throw new Error('Сервер вернул пустой ответ.')
  return body as T
}

function progressPercent(done: number, total: number): number {
  if (total <= 0) return 0
  return Math.max(0, Math.min(100, Math.round((done / total) * 100)))
}

function formatDate(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat('ru-RU', { day: 'numeric', month: 'long', year: 'numeric' }).format(date)
}

function makeIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  const random = new Uint32Array(4)
  if (typeof crypto !== 'undefined' && crypto.getRandomValues) crypto.getRandomValues(random)
  return Array.from(random).map((part) => part.toString(16)).join('-') + '-' + Date.now().toString(36)
}

function modeLabel(mode: string): string {
  return mode === 'curator' ? 'С координатором' : 'Самостоятельно'
}

function courseRunStatus(status: string): string {
  switch (status) {
    case 'completed': return 'Завершён'
    case 'in_progress': return 'В процессе'
    case 'awaiting_curator': return 'Ожидает проверки'
    case 'paused': return 'Приостановлен'
    default: return 'Статус неизвестен'
  }
}

function lessonStatus(status: string): string {
  switch (status) {
    case 'completed': return 'Выполнено'
    case 'in_progress': return 'Продолжить'
    case 'awaiting_curator': return 'На проверке'
    case 'paused': return 'Приостановлено'
    default: return 'Начать урок'
  }
}

function AppHeader({ displayName, subtitle, onBack }: { displayName: string; subtitle: string; onBack?: () => void }) {
  return (
    <header className="mini-header">
      <div className="mini-brand">
        <div className="mini-brand-mark" aria-hidden="true"><span /><span /><span /></div>
        <div><strong>Слово</strong><small>ОБУЧАЮЩАЯ ПЛАТФОРМА</small></div>
      </div>
      <div className="mini-header-right">
        {onBack && <button className="mini-back" type="button" onClick={onBack} aria-label="Назад">←</button>}
        <div className="mini-avatar" aria-label={displayName}>{(displayName.trim()[0] || 'У').toLocaleUpperCase('ru-RU')}</div>
      </div>
      <div className="mini-header-subtitle">{subtitle}</div>
    </header>
  )
}

function App() {
  const webApp = window.Telegram?.WebApp
  const initData = webApp?.initData || ''
  const [bootstrap, setBootstrap] = useState<Bootstrap | null>(null)
  const [view, setView] = useState<View>('account')
  const [tab, setTab] = useState<'account' | 'catalog'>('account')
  const [courseDetails, setCourseDetails] = useState<CourseDetails | null>(null)
  const [runProgress, setRunProgress] = useState<CourseRun | null>(null)
  const [runLessons, setRunLessons] = useState<RunLesson[]>([])
  const [activeLessonID, setActiveLessonID] = useState<number | null>(null)
  const [lessonState, setLessonState] = useState<LessonQuestionsState | null>(null)
  const [answerDrafts, setAnswerDrafts] = useState<Record<number, AnswerDraft>>({})
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  const api = useCallback(<T,>(path: string, init?: RequestInit) => {
    if (!initData) return Promise.reject(new Error('Откройте Mini App из Telegram, чтобы войти в личный кабинет.'))
    return requestJSON<T>(initData, path, init)
  }, [initData])

  const loadBootstrap = useCallback(async () => {
    const data = await api<Bootstrap>('/api/v1/miniapp/bootstrap')
    setBootstrap(data)
    return data
  }, [api])

  useEffect(() => {
    webApp?.ready()
    webApp?.expand()
    webApp?.setHeaderColor?.('#f5f7f2')
    webApp?.setBackgroundColor?.('#f5f7f2')
    webApp?.disableVerticalSwipes?.()
    if (!initData) {
      setLoading(false)
      return
    }
    let cancelled = false
    api<Bootstrap>('/api/v1/miniapp/bootstrap')
      .then((data) => { if (!cancelled) setBootstrap(data) })
      .catch((reason: unknown) => { if (!cancelled) setError(reason instanceof Error ? reason.message : 'Не удалось загрузить приложение.') })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [api, initData, webApp])

  const perform = async (action: () => Promise<void>) => {
    setBusy(true)
    setError('')
    setNotice('')
    try {
      await action()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Не удалось выполнить действие.')
    } finally {
      setBusy(false)
    }
  }

  const openCatalog = () => {
    setTab('catalog')
    setView('catalog')
    setError('')
    setNotice('')
  }

  const openAccount = () => {
    setTab('account')
    setView('account')
    setError('')
    setNotice('')
  }

  const loadRunLessons = async (runID: number) => {
    const result = await api<{ lessons: RunLesson[] }>('/api/v1/miniapp/course-runs/' + runID + '/lessons')
    setRunLessons(result.lessons || [])
  }

  const continueCourse = () => {
    const active = bootstrap?.active_course
    if (!active) return
    if (active.enrollment.status === 'awaiting_curator') {
      setNotice('Курс ожидает проверки ответов координатором. Новые уроки пока недоступны.')
      return
    }

    void perform(async () => {
      let run = active
      if (active.enrollment.status === 'paused') {
        run = await api<CourseRun>('/api/v1/miniapp/courses/' + active.enrollment.course_id + '/start', {
          method: 'POST',
          body: JSON.stringify({ mode: active.enrollment.mode }),
        })
      }

      const [details, lessonResult] = await Promise.all([
        api<CourseDetails>('/api/v1/miniapp/courses/' + run.enrollment.course_id),
        api<{ lessons: RunLesson[] }>('/api/v1/miniapp/course-runs/' + run.enrollment.id + '/lessons'),
      ])
      setRunProgress(run)
      setRunLessons(lessonResult.lessons || [])
      setCourseDetails(details)
      setTab('catalog')
      setView('lessons')

      if (active.enrollment.status === 'paused') {
        setBootstrap((previous) => previous ? { ...previous, active_course: run } : previous)
        setNotice('Курс возобновлён.')
      }
    })
  }

  const openCourse = (courseID: number) => {
    const active = bootstrap?.active_course
    if (active && active.enrollment.course_id !== courseID) {
      setError('Сначала завершите текущий курс «' + active.enrollment.course_title + '». После этого можно будет выбрать другой.')
      return
    }
    if (active && active.enrollment.status !== 'awaiting_curator') {
      continueCourse()
      return
    }

    void perform(async () => {
      const data = await api<CourseDetails>('/api/v1/miniapp/courses/' + courseID)
      const current = data.active_course
      if (current && current.enrollment.course_id !== courseID) {
        setBootstrap((previous) => previous ? { ...previous, active_course: current } : previous)
        setRunProgress(current)
        setView('catalog')
        setError('Сейчас вы проходите курс «' + current.enrollment.course_title + '». Сначала завершите его.')
        return
      }
      setCourseDetails(data)
      setRunProgress(current || null)
      setView('course')
    })
  }

  const startCourse = (mode: 'ai' | 'curator') => {
    if (!courseDetails) return
    const active = bootstrap?.active_course
    if (active) {
      if (active.enrollment.course_id === courseDetails.course.id) {
        continueCourse()
      } else {
        setError('Сначала завершите текущий курс «' + active.enrollment.course_title + '».')
      }
      return
    }

    void perform(async () => {
      const run = await api<CourseRun>('/api/v1/miniapp/courses/' + courseDetails.course.id + '/start', {
        method: 'POST',
        body: JSON.stringify({ mode }),
      })
      const [, updated] = await Promise.all([
        loadRunLessons(run.enrollment.id),
        loadBootstrap(),
      ])
      setRunProgress(run)
      setView('lessons')
      setBootstrap(updated)
    })
  }

  const startLesson = (lessonID: number) => {
    if (!runProgress) return
    void perform(async () => {
      const state = await api<LessonQuestionsState>(
        '/api/v1/miniapp/course-runs/' + runProgress.enrollment.id + '/lessons/' + lessonID + '/start',
        { method: 'POST', body: '{}' },
      )
      const initialDrafts: Record<number, AnswerDraft> = {}
      for (const question of state.questions || []) {
        initialDrafts[question.id] = {
          answerText: question.saved_answer?.answer_text || '',
          selectedOptionIDs: question.saved_answer?.selected_option_ids || [],
          dirty: false,
        }
      }
      setAnswerDrafts((previous) => {
        if (activeLessonID !== lessonID) return initialDrafts
        const unsavedDrafts = Object.fromEntries(
          Object.entries(previous).filter(([, draft]) => draft.dirty),
        ) as Record<number, AnswerDraft>
        return { ...initialDrafts, ...unsavedDrafts }
      })
      setActiveLessonID(lessonID)
      setLessonState(state)
      setView('lesson')
    })
  }

  const saveAnswer = (questionID: number) => {
    if (!lessonState) return
    const question = lessonState.questions.find((item) => item.id === questionID)
    if (!question) return
    const draft = answerDrafts[questionID] || {
      answerText: question.saved_answer?.answer_text || '',
      selectedOptionIDs: question.saved_answer?.selected_option_ids || [],
      dirty: false,
    }
    if (question.answer_type === 'text' && !draft.answerText.trim()) {
      setError('Напишите ответ перед отправкой.')
      return
    }
    if (question.answer_type !== 'text' && draft.selectedOptionIDs.length === 0) {
      setError('Выберите вариант ответа.')
      return
    }
    void perform(async () => {
      const saved = await api<QuestionState>(
        '/api/v1/miniapp/lesson-enrollments/' + lessonState.progress.enrollment.id + '/questions/' + question.id + '/answer',
        {
          method: 'POST',
          body: JSON.stringify({
            answer_text: question.answer_type === 'text' ? draft.answerText.trim() : '',
            selected_option_ids: question.answer_type === 'text' ? [] : draft.selectedOptionIDs,
            idempotency_key: makeIdempotencyKey(),
          }),
        },
      )
      if (!saved.question) throw new Error('Сервер не вернул сохранённый вопрос.')
      setLessonState((current) => current ? {
        ...current,
        progress: saved.progress,
        questions: current.questions.map((item) => item.id === question.id ? saved.question! : item),
      } : current)
      setAnswerDrafts((current) => ({
        ...current,
        [question.id]: {
          answerText: question.answer_type === 'text' ? draft.answerText.trim() : '',
          selectedOptionIDs: question.answer_type === 'text' ? [] : [...draft.selectedOptionIDs],
          dirty: false,
        },
      }))
      setNotice('Ответ на вопрос ' + question.number + ' сохранён.')
    })
  }

  const finishLesson = () => {
    if (!lessonState || !allQuestionsAnswered) {
      setError('Сначала сохраните ответы на все вопросы урока.')
      return
    }
    void perform(async () => {
      await api('/api/v1/miniapp/lesson-enrollments/' + lessonState.progress.enrollment.id + '/complete', {
        method: 'POST',
      })
      if (runProgress) await loadRunLessons(runProgress.enrollment.id)
      const updated = await loadBootstrap()
      setBootstrap(updated)
      setView('lessons')
      setNotice(runProgress?.enrollment.mode === 'curator'
        ? 'Урок отправлен координатору. Он проверит ваши ответы и при необходимости свяжется с вами.'
        : 'Урок завершён. Отличная работа!')
      setLessonState(null)
      setAnswerDrafts({})
      setActiveLessonID(null)
    })
  }

  const back = () => {
    setError('')
    if (view === 'lesson') setView('lessons')
    else if (view === 'lessons') setView('course')
    else if (view === 'mode') setView('course')
    else if (view === 'course') setView('catalog')
    else if (view !== 'account') setView('account')
  }

  if (!initData) {
    return (
      <main className="mini-app mini-auth-required">
        <div className="mini-auth-card">
          <div className="mini-brand-mark mini-brand-mark-large"><span /><span /><span /></div>
          <p className="mini-overline">СЛОВО · ОБУЧАЮЩАЯ ПЛАТФОРМА</p>
          <h1>Откройте приложение в Telegram</h1>
          <p>Личный кабинет и прохождение уроков доступны после запуска Mini App из чата нашего Telegram-бота.</p>
        </div>
      </main>
    )
  }

  const displayName = bootstrap?.student.display_name || 'Ученик'
  const currentCourse = bootstrap?.active_course
  const currentCourseStats = currentCourse
    ? bootstrap?.cabinet.courses.find((course) => course.course_id === currentCourse.enrollment.course_id)
    : undefined
  const courseHistory = (bootstrap?.cabinet.courses || []).filter(
    (course) => course.course_id !== currentCourse?.enrollment.course_id,
  )
  const selectedCourse = courseDetails?.course
  const activeLesson = courseDetails?.lessons.find((lesson) => lesson.id === activeLessonID)
  const lessonTitle = runLessons.find((lesson) => lesson.lesson_id === activeLessonID)?.title || selectedCourse?.title || 'Урок'
  const answeredQuestionCount = lessonState?.questions.filter((question) => {
    const draft = answerDrafts[question.id]
    return Boolean(question.saved_answer && !draft?.dirty)
  }).length || 0
  const allQuestionsAnswered = Boolean(lessonState && (
    lessonState.questions.length === 0
      ? lessonState.at_end
      : lessonState.questions.every((question) => Boolean(question.saved_answer && !answerDrafts[question.id]?.dirty))
  ))
  const completedRunLessons = runLessons.filter((lesson) => lesson.status === 'completed').length
  const remainingRunLessons = runLessons.length - completedRunLessons

  return (
    <main className="mini-app">
      <AppHeader displayName={displayName} subtitle={
        view === 'account' ? 'ЛИЧНЫЙ КАБИНЕТ'
          : view === 'catalog' ? 'БИБЛЕЙСКИЕ КУРСЫ'
            : view === 'lesson' ? lessonTitle
              : selectedCourse?.title || 'ОБУЧЕНИЕ'
      } onBack={view !== 'account' && view !== 'catalog' ? back : undefined} />

      <div className="mini-content">
        {loading && <div className="mini-loading"><span className="mini-spinner" />Загружаем ваши данные…</div>}

        {!loading && bootstrap && view === 'account' && (
          <section className="mini-page">
            <div className="mini-welcome">
              <p className="mini-overline">РАДЫ ВАС ВИДЕТЬ</p>
              <h1>Здравствуйте, {displayName.split(' ')[0]}!</h1>
              <p>Продолжайте изучать Библию в удобном для вас темпе.</p>
            </div>

            <div className="mini-stats">
              <article className="mini-stat-card"><span>Завершено курсов</span><strong>{bootstrap.cabinet.completed_courses}</strong><small>Ваш результат</small></article>
              <article className="mini-stat-card"><span>Пройдено уроков</span><strong>{bootstrap.cabinet.completed_lessons}</strong><small>Шаг за шагом</small></article>
            </div>

            {bootstrap.cabinet.unread_conversation_messages > 0 && (
              <div className="mini-message-note"><span>✦</span><div><strong>Новое сообщение</strong><small>У вас {bootstrap.cabinet.unread_conversation_messages} непрочитанных сообщений от координатора.</small></div></div>
            )}

            <section className="mini-panel mini-current-panel">
              <div className="mini-panel-heading"><div><p className="mini-overline">ВАШЕ ОБУЧЕНИЕ</p><h2>Текущий курс</h2></div><span className="mini-icon-bubble">✦</span></div>
              {currentCourse ? (
                <>
                  <h3>{currentCourse.enrollment.course_title}</h3>
                  <div className="mini-course-meta"><span>{modeLabel(currentCourse.enrollment.mode)}</span><span>{currentCourse.completed_lessons} из {currentCourse.total_lessons} уроков</span></div>
                  <div className="mini-progress"><span style={{ width: progressPercent(currentCourse.completed_lessons, currentCourse.total_lessons) + '%' }} /></div>
                  <div className="mini-progress-caption"><span>Ваш прогресс</span><strong>{progressPercent(currentCourse.completed_lessons, currentCourse.total_lessons)}%</strong></div>
                  {currentCourse.enrollment.status === 'awaiting_curator' ? (
                    <div className="mini-course-state-note">
                      <strong>Курс ожидает проверки</strong>
                      <p>Координатор проверяет отправленные ответы. Продолжить уроки можно будет после проверки.</p>
                      {currentCourseStats && currentCourseStats.pending_reviews > 0 && (
                        <small>Ответов ожидают проверки: {currentCourseStats.pending_reviews}</small>
                      )}
                      {currentCourseStats && currentCourseStats.needs_revision_answers > 0 && (
                        <small>Ответов требуют доработки: {currentCourseStats.needs_revision_answers}</small>
                      )}
                    </div>
                  ) : (
                    <button className="mini-primary-button mini-full-button" type="button" onClick={continueCourse} disabled={busy}>
                      {currentCourse.enrollment.status === 'paused' ? 'Возобновить курс' : 'Продолжить курс'} <span>→</span>
                    </button>
                  )}
                </>
              ) : (
                <div className="mini-empty">
                  <div className="mini-empty-icon">⌂</div>
                  <h3>Вы ещё не начали обучение</h3>
                  <p>Выберите курс, который хотите изучить, и начните с первого урока.</p>
                  <button className="mini-primary-button" type="button" onClick={openCatalog}>Посмотреть курсы <span>→</span></button>
                </div>
              )}
            </section>

            <section className="mini-panel mini-profile-panel">
              <p className="mini-overline">ВАШ ПРОФИЛЬ</p>
              <div className="mini-profile-row"><span>Дата регистрации</span><strong>{formatDate(bootstrap.cabinet.registered_at)}</strong></div>
              <div className="mini-profile-row"><span>Формат обучения</span><strong>{currentCourse ? modeLabel(currentCourse.enrollment.mode) : 'Не выбран'}</strong></div>
            </section>

            {courseHistory.length > 0 && (
              <section className="mini-panel mini-history-panel">
                <div className="mini-panel-heading">
                  <div><p className="mini-overline">ВАШ ПРОГРЕСС</p><h2>История курсов</h2></div>
                  <span className="mini-count">{courseHistory.length}</span>
                </div>
                <div className="mini-history-list">
                  {courseHistory.map((course) => (
                    <article className="mini-history-course" key={course.course_id}>
                      <div className="mini-history-heading">
                        <strong>{course.title}</strong>
                        <span className="mini-history-status">{courseRunStatus(course.status)}</span>
                      </div>
                      <div className="mini-course-meta">
                        <span>{modeLabel(course.mode)}</span>
                        <span>{course.completed_lessons} из {course.total_lessons} уроков</span>
                      </div>
                      <div className="mini-progress">
                        <span style={{ width: progressPercent(course.completed_lessons, course.total_lessons) + '%' }} />
                      </div>
                      {(course.pending_reviews > 0 || course.needs_revision_answers > 0) && (
                        <div className="mini-history-details">
                          {course.pending_reviews > 0 && <small>Ожидают проверки: {course.pending_reviews}</small>}
                          {course.needs_revision_answers > 0 && <small>Требуют доработки: {course.needs_revision_answers}</small>}
                        </div>
                      )}
                    </article>
                  ))}
                </div>
              </section>
            )}
          </section>
        )}

        {!loading && bootstrap && view === 'catalog' && (
          <section className="mini-page">
            <div className="mini-welcome mini-welcome-compact">
              <p className="mini-overline">ОТКРОЙТЕ ДЛЯ СЕБЯ</p>
              <h1>Курсы Библии</h1>
              <p>Выберите направление и проходите уроки последовательно.</p>
            </div>
            {currentCourse && (
              <div className="mini-warning-note mini-catalog-warning">
                <strong>У вас уже есть активный курс</strong>
                <p>«{currentCourse.enrollment.course_title}». Остальные курсы станут доступны после его завершения.</p>
                <button className="mini-primary-button" type="button" onClick={() => openCourse(currentCourse.enrollment.course_id)} disabled={busy}>
                  {currentCourse.enrollment.status === 'awaiting_curator'
                    ? 'Посмотреть состояние курса'
                    : currentCourse.enrollment.status === 'paused'
                      ? 'Возобновить текущий курс'
                      : 'Продолжить текущий курс'} →
                </button>
              </div>
            )}
            {bootstrap.courses.length === 0 ? (
              <div className="mini-panel mini-empty"><div className="mini-empty-icon">✧</div><h3>Курсы скоро появятся</h3><p>Пока нет опубликованных курсов. Загляните сюда немного позже.</p></div>
            ) : (
              <div className="mini-course-grid">
                {bootstrap.courses.map((course, index) => {
                  const isCurrentCourse = currentCourse?.enrollment.course_id === course.id
                  const isLocked = Boolean(currentCourse && !isCurrentCourse)
                  return (
                    <button
                      className={'mini-course-card' + (isLocked ? ' is-locked' : '')}
                      type="button"
                      key={course.id}
                      onClick={() => openCourse(course.id)}
                      disabled={busy || isLocked}
                      aria-label={isLocked
                        ? course.title + '. Недоступен до завершения текущего курса.'
                        : course.title + '. Открыть курс.'}
                    >
                      <div className={'mini-course-art mini-course-art-' + (index % 4)}>
                        <span className="mini-art-symbol">{['✧', '⌖', '✦', '❋'][index % 4]}</span>
                        <span className="mini-art-label">КУРС {String(index + 1).padStart(2, '0')}</span>
                      </div>
                      <div className="mini-course-card-body">
                        <span className="mini-course-card-meta">
                          {course.lesson_count} {course.lesson_count === 1 ? 'урок' : course.lesson_count >= 2 && course.lesson_count <= 4 ? 'урока' : 'уроков'}
                          {isCurrentCourse ? ' · Текущий курс' : isLocked ? ' · Временно недоступен' : ''}
                        </span>
                        <h3>{course.title}</h3>
                        <p>{course.description || 'Последовательное изучение библейских тем и текстов.'}</p>
                        <span className="mini-course-open">
                          {isCurrentCourse ? 'Открыть текущий курс' : isLocked ? 'Сначала завершите текущий' : 'Открыть курс'}
                          <b>{isLocked ? '⌑' : '→'}</b>
                        </span>
                      </div>
                    </button>
                  )
                })}
              </div>
            )}
          </section>
        )}

        {!loading && view === 'course' && selectedCourse && (
          <section className="mini-page">
            <div className="mini-course-hero">
              <p className="mini-overline">ПРОГРАММА ОБУЧЕНИЯ</p>
              <h1>{selectedCourse.title}</h1>
              <p>{selectedCourse.description || 'Изучайте библейские темы последовательно, переходя от одного урока к следующему.'}</p>
              <div className="mini-hero-facts"><span>▤ {selectedCourse.lesson_count} уроков</span><span>✧ В своём темпе</span></div>
            </div>
            {runProgress && runProgress.enrollment.course_id !== selectedCourse.id && (
              <div className="mini-warning-note">
                <strong>У вас уже есть незавершённый курс</strong>
                <p>Сначала завершите «{runProgress.enrollment.course_title}», чтобы перейти к новому курсу.</p>
                <button className="mini-primary-button" type="button" onClick={continueCourse}>Продолжить текущий курс →</button>
              </div>
            )}
            <section className="mini-panel">
              <div className="mini-panel-heading"><div><p className="mini-overline">СОДЕРЖАНИЕ</p><h2>Уроки курса</h2></div><span className="mini-count">{courseDetails?.lessons.length || 0}</span></div>
              <div className="mini-preview-lessons">
                {courseDetails?.lessons.map((lesson, index) => (
                  <div className="mini-preview-lesson" key={lesson.id}>
                    <span className="mini-lesson-number">{String(index + 1).padStart(2, '0')}</span>
                    <div><strong>{lesson.title}</strong><small>{lesson.question_count} вопросов</small></div>
                    <span className="mini-lock">•</span>
                  </div>
                ))}
              </div>
            </section>
            <div className="mini-bottom-action">
              {runProgress && runProgress.enrollment.course_id !== selectedCourse.id ? (
                <button className="mini-primary-button mini-full-button" type="button" disabled>
                  Сначала завершите текущий курс
                </button>
              ) : runProgress?.enrollment.status === 'awaiting_curator' ? (
                <button className="mini-primary-button mini-full-button" type="button" disabled>
                  Курс ожидает проверки
                </button>
              ) : runProgress?.enrollment.status === 'paused' ? (
                <button className="mini-primary-button mini-full-button" type="button" onClick={continueCourse} disabled={busy}>
                  Возобновить курс <span>→</span>
                </button>
              ) : runProgress ? (
                <button className="mini-primary-button mini-full-button" type="button" onClick={continueCourse} disabled={busy}>
                  Продолжить курс <span>→</span>
                </button>
              ) : currentCourse ? (
                <button className="mini-primary-button mini-full-button" type="button" disabled>
                  Сначала завершите текущий курс
                </button>
              ) : (
                <button className="mini-primary-button mini-full-button" type="button" onClick={() => setView('mode')} disabled={busy}>
                  Начать обучение <span>→</span>
                </button>
              )}
            </div>
          </section>
        )}

        {!loading && view === 'mode' && selectedCourse && (
          <section className="mini-page">
            <div className="mini-welcome">
              <p className="mini-overline">ВАШ ВЫБОР</p>
              <h1>Как вы хотите учиться?</h1>
              <p>Выберите удобный формат. Изменить его после начала курса нельзя.</p>
            </div>
            <button className="mini-mode-card" type="button" onClick={() => startCourse('curator')} disabled={busy}>
              <span className="mini-mode-icon">✦</span>
              <span className="mini-mode-copy"><strong>С координатором</strong><small>За вами закрепится человек, который поможет с прохождением и ответит на вопросы. Всё обучение остаётся анонимным.</small><b>Выбрать формат <span>→</span></b></span>
            </button>
            <button className="mini-mode-card" type="button" onClick={() => startCourse('ai')} disabled={busy}>
              <span className="mini-mode-icon mini-mode-icon-light">◌</span>
              <span className="mini-mode-copy"><strong>Самостоятельно</strong><small>Проходите уроки в своём темпе. Ответы сохраняются автоматически, а при необходимости можно связаться с координатором.</small><b>Выбрать формат <span>→</span></b></span>
            </button>
            <p className="mini-privacy-note">Ваш выбор сохранится для всего курса.</p>
          </section>
        )}

        {!loading && view === 'lessons' && runProgress && (
          <section className="mini-page">
            <div className="mini-welcome">
              <p className="mini-overline">ВАШ ПУТЬ</p>
              <h1>{runProgress.enrollment.course_title}</h1>
              <p>{modeLabel(runProgress.enrollment.mode)} · двигайтесь последовательно, урок за уроком.</p>
            </div>
            <div className="mini-panel mini-run-progress">
              <div className="mini-progress-caption"><span>Прогресс курса</span><strong>{progressPercent(completedRunLessons, runProgress.total_lessons)}%</strong></div>
              <div className="mini-progress"><span style={{ width: progressPercent(completedRunLessons, runProgress.total_lessons) + '%' }} /></div>
              <div className="mini-course-meta"><span>{completedRunLessons} завершено</span><span>{remainingRunLessons} осталось</span></div>
            </div>
            <section className="mini-panel">
              <div className="mini-panel-heading"><div><p className="mini-overline">ПОСЛЕДОВАТЕЛЬНОСТЬ</p><h2>Уроки курса</h2></div><span className="mini-count">{runLessons.length}</span></div>
              <div className="mini-run-lessons">
                {runLessons.filter((lesson) => lesson.status !== 'completed').map((lesson, index) => {
                  const waiting = lesson.status === 'awaiting_curator'
                  return (
                    <button className="mini-run-lesson" type="button" key={lesson.lesson_id}
                      onClick={() => startLesson(lesson.lesson_id)} disabled={waiting || busy}>
                      <span className="mini-run-number">{String(index + 1).padStart(2, '0')}</span>
                      <span className="mini-run-copy"><strong>{lesson.title}</strong><small>{lesson.answered_questions} из {lesson.total_questions} вопросов · {lessonStatus(lesson.status)}</small></span>
                      <span className="mini-run-arrow">{waiting ? '◷' : '→'}</span>
                    </button>
                  )
                })}
              </div>
              {runLessons.filter((lesson) => lesson.status !== 'completed').length === 0 && (
                <p className="mini-empty-inline">
                  {runLessons.length === 0
                    ? 'В этом курсе пока нет доступных уроков.'
                    : runProgress.enrollment.mode === 'curator'
                      ? 'Все уроки отправлены координатору. Он проверит ответы и при необходимости свяжется с вами.'
                      : 'Вы завершили все уроки курса.'}
                </p>
              )}
            </section>
            {runProgress.is_complete && <div className="mini-success-note">
              {runProgress.enrollment.mode === 'curator'
                ? 'Все уроки отправлены координатору. Ожидайте проверки ответов.'
                : 'Вы завершили все уроки курса. Можно вернуться в каталог и выбрать следующий курс.'}
            </div>}
          </section>
        )}

        {!loading && view === 'lesson' && lessonState && (
          <section className="mini-page mini-lesson-page">
            <div className="mini-lesson-progress-head">
              <div><p className="mini-overline">{lessonTitle}</p><h1>Прохождение урока</h1></div>
              <span>{answeredQuestionCount} / {lessonState.questions.length} ответов</span>
            </div>
            <div className="mini-progress"><span style={{ width: progressPercent(answeredQuestionCount, lessonState.questions.length) + '%' }} /></div>

            {activeLesson?.description?.trim() && (
              <section className="mini-panel mini-lesson-preface">
                <p className="mini-overline">ПРЕДИСЛОВИЕ</p>
                <div className="mini-lesson-preface-text">{activeLesson.description}</div>
              </section>
            )}

            {lessonState.questions.map((question) => {
              const draft = answerDrafts[question.id] || {
                answerText: question.saved_answer?.answer_text || '',
                selectedOptionIDs: question.saved_answer?.selected_option_ids || [],
                dirty: false,
              }
              const isSaved = Boolean(question.saved_answer && !draft.dirty)
              return (
                <article className="mini-panel mini-question-card" key={question.id}>
                  <div className="mini-question-label">ВОПРОС {question.number}</div>
                  <h2>{question.prompt}</h2>

                  {question.bible_passages.length > 0 && (
                    <div className="mini-scripture-list">
                      {question.bible_passages.map((passage, index) => (
                        <div className="mini-scripture" key={index}>
                          <span>БИБЛЕЙСКИЙ ОТРЫВОК</span>
                          <strong>{passage.book_name} {passage.chapter}:{passage.start_verse}{passage.end_verse > passage.start_verse ? '–' + passage.end_verse : ''}</strong>
                          {passage.passage_text && <p>{passage.passage_text}</p>}
                        </div>
                      ))}
                    </div>
                  )}

                  {question.answer_type === 'text' ? (
                    <label className="mini-answer-field">
                      <span>Ваш ответ</span>
                      <textarea
                        rows={5}
                        value={draft.answerText}
                        placeholder="Напишите свой ответ…"
                        onChange={(event) => {
                          setAnswerDrafts((current) => ({
                            ...current,
                            [question.id]: { ...draft, answerText: event.target.value, dirty: true },
                          }))
                          setError('')
                        }}
                      />
                    </label>
                  ) : (
                    <div className="mini-answer-options">
                      <p>{question.answer_type === 'multiple_choice' ? 'Выберите все подходящие варианты' : 'Выберите один вариант'}</p>
                      {question.options.map((option) => {
                        const selected = draft.selectedOptionIDs.includes(option.id)
                        return (
                          <button
                            className={'mini-answer-option' + (selected ? ' is-selected' : '')}
                            type="button"
                            key={option.id}
                            onClick={() => {
                              setAnswerDrafts((current) => {
                                const currentDraft = current[question.id] || draft
                                const selectedOptionIDs = question.answer_type === 'single_choice'
                                  ? [option.id]
                                  : selected
                                    ? currentDraft.selectedOptionIDs.filter((id) => id !== option.id)
                                    : [...currentDraft.selectedOptionIDs, option.id]
                                return {
                                  ...current,
                                  [question.id]: { ...currentDraft, selectedOptionIDs, dirty: true },
                                }
                              })
                              setError('')
                            }}
                          >
                            <span className="mini-option-check">{selected ? '✓' : ''}</span><span>{option.text}</span>
                          </button>
                        )
                      })}
                    </div>
                  )}

                  {question.latest_attempt?.review_feedback && (
                    <div className="mini-feedback-note">
                      <strong>{question.latest_attempt.review_decision === 'needs_revision' ? 'Комментарий координатора' : 'Ответ проверен'}</strong>
                      <p>{question.latest_attempt.review_feedback}</p>
                    </div>
                  )}

                  {isSaved && <div className="mini-saved-note"><span>✓</span> Ответ сохранён</div>}
                  <button className="mini-primary-button mini-full-button" type="button" onClick={() => saveAnswer(question.id)} disabled={busy}>
                    {busy ? 'Сохраняем…' : question.saved_answer ? 'Обновить ответ' : 'Сохранить ответ'} <span>→</span>
                  </button>
                </article>
              )
            })}

            <div className="mini-panel mini-finish-card">
              <div className="mini-finish-icon">✦</div>
              <p className="mini-overline">ЗАВЕРШЕНИЕ УРОКА</p>
              <h2>{allQuestionsAnswered ? 'Все ответы сохранены' : 'Ответьте на вопросы в удобном порядке'}</h2>
              <p>Сохранено ответов: {answeredQuestionCount} из {lessonState.questions.length}. Каждый ответ можно сохранить отдельно.</p>
              {!allQuestionsAnswered && <p>Чтобы отправить урок дальше, сначала сохраните ответ на каждый вопрос.</p>}
              <button className="mini-primary-button mini-full-button" type="button" onClick={finishLesson} disabled={busy || !allQuestionsAnswered}>
                Завершить урок <span>→</span>
              </button>
            </div>
          </section>
        )}

        {error && <div className="mini-alert" role="alert"><span>!</span><p>{error}</p><button type="button" onClick={() => setError('')} aria-label="Закрыть">×</button></div>}
        {notice && <div className="mini-toast" role="status"><span>✓</span>{notice}<button type="button" onClick={() => setNotice('')} aria-label="Закрыть">×</button></div>}
        {busy && <div className="mini-busy-line" aria-label="Выполняется запрос"><span /></div>}
      </div>

      <nav className="mini-tabbar" aria-label="Основная навигация">
        <button type="button" className={tab === 'account' ? 'is-active' : ''} onClick={openAccount}>
          <span className="mini-tab-icon">◉</span><strong>Личный кабинет</strong>
        </button>
        <button type="button" className={tab === 'catalog' ? 'is-active' : ''} onClick={openCatalog}>
          <span className="mini-tab-icon">▤</span><strong>Курсы</strong>
        </button>
      </nav>
    </main>
  )
}

export default App
