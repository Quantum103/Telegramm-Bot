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
  const [questionState, setQuestionState] = useState<QuestionState | null>(null)
  const [answerText, setAnswerText] = useState('')
  const [selectedOptions, setSelectedOptions] = useState<number[]>([])
  const [answerDirty, setAnswerDirty] = useState(false)
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

  const openCourse = (courseID: number) => perform(async () => {
    const data = await api<CourseDetails>('/api/v1/miniapp/courses/' + courseID)
    setCourseDetails(data)
    if (data.active_course?.enrollment.course_id === courseID && data.active_course.enrollment.status === 'in_progress') {
      setRunProgress(data.active_course)
      await loadRunLessons(data.active_course.enrollment.id)
      setView('lessons')
    } else {
      setRunProgress(data.active_course || null)
      setView('course')
    }
  })

  const continueCourse = () => {
    if (!bootstrap?.active_course) return
    void perform(async () => {
      const active = bootstrap.active_course!
      setRunProgress(active)
      await loadRunLessons(active.enrollment.id)
      const details = await api<CourseDetails>('/api/v1/miniapp/courses/' + active.enrollment.course_id)
      setCourseDetails(details)
      setTab('catalog')
      setView('lessons')
    })
  }

  const startCourse = (mode: 'ai' | 'curator') => {
    if (!courseDetails) return
    void perform(async () => {
      const run = await api<CourseRun>('/api/v1/miniapp/courses/' + courseDetails.course.id + '/start', {
        method: 'POST',
        body: JSON.stringify({ mode }),
      })
      setRunProgress(run)
      await loadRunLessons(run.enrollment.id)
      setView('lessons')
      const updated = await loadBootstrap()
      setBootstrap(updated)
    })
  }

  const startLesson = (lessonID: number) => {
    if (!runProgress) return
    void perform(async () => {
      const state = await api<QuestionState>(
        '/api/v1/miniapp/course-runs/' + runProgress.enrollment.id + '/lessons/' + lessonID + '/start',
        { method: 'POST', body: '{}' },
      )
      setActiveLessonID(lessonID)
      setQuestionState(state)
      setAnswerText(state.question?.saved_answer?.answer_text || '')
      setSelectedOptions(state.question?.saved_answer?.selected_option_ids || [])
      setAnswerDirty(false)
      setView('lesson')
    })
  }

  const saveAnswer = () => {
    if (!questionState?.question) return
    const question = questionState.question
    if (question.answer_type === 'text' && !answerText.trim()) {
      setError('Напишите ответ перед отправкой.')
      return
    }
    if (question.answer_type !== 'text' && selectedOptions.length === 0) {
      setError('Выберите вариант ответа.')
      return
    }
    void perform(async () => {
      const state = await api<QuestionState>(
        '/api/v1/miniapp/lesson-enrollments/' + questionState.progress.enrollment.id + '/questions/' + question.id + '/answer',
        {
          method: 'POST',
          body: JSON.stringify({
            answer_text: question.answer_type === 'text' ? answerText.trim() : '',
            selected_option_ids: question.answer_type === 'text' ? [] : selectedOptions,
            idempotency_key: makeIdempotencyKey(),
          }),
        },
      )
      setQuestionState(state)
      setAnswerDirty(false)
      setNotice('Ответ сохранён.')
    })
  }

  const navigateQuestion = (direction: 'next' | 'previous') => {
    if (!questionState) return
    void perform(async () => {
      const state = await api<QuestionState>(
        '/api/v1/miniapp/lesson-enrollments/' + questionState.progress.enrollment.id + '/questions/navigate',
        { method: 'POST', body: JSON.stringify({ direction }) },
      )
      setQuestionState(state)
      setAnswerText(state.question?.saved_answer?.answer_text || '')
      setSelectedOptions(state.question?.saved_answer?.selected_option_ids || [])
      setAnswerDirty(false)
    })
  }

  const finishLesson = () => {
    if (!questionState) return
    void perform(async () => {
      await api('/api/v1/miniapp/lesson-enrollments/' + questionState.progress.enrollment.id + '/complete', {
        method: 'POST',
      })
      if (runProgress) await loadRunLessons(runProgress.enrollment.id)
      const updated = await loadBootstrap()
      setBootstrap(updated)
      setView('lessons')
      setNotice(runProgress?.enrollment.mode === 'curator'
        ? 'Урок отправлен координатору. Он проверит ваши ответы и при необходимости свяжется с вами.'
        : 'Урок завершён. Отличная работа!')
      setQuestionState(null)
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
  const selectedCourse = courseDetails?.course
  const activeQuestion = questionState?.question
  const lessonTitle = runLessons.find((lesson) => lesson.lesson_id === activeLessonID)?.title || selectedCourse?.title || 'Урок'
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
                  <button className="mini-primary-button mini-full-button" type="button" onClick={continueCourse} disabled={busy}>Продолжить курс <span>→</span></button>
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
          </section>
        )}

        {!loading && bootstrap && view === 'catalog' && (
          <section className="mini-page">
            <div className="mini-welcome mini-welcome-compact">
              <p className="mini-overline">ОТКРОЙТЕ ДЛЯ СЕБЯ</p>
              <h1>Курсы Библии</h1>
              <p>Выберите направление и проходите уроки последовательно.</p>
            </div>
            {bootstrap.courses.length === 0 ? (
              <div className="mini-panel mini-empty"><div className="mini-empty-icon">✧</div><h3>Курсы скоро появятся</h3><p>Пока нет опубликованных курсов. Загляните сюда немного позже.</p></div>
            ) : (
              <div className="mini-course-grid">
                {bootstrap.courses.map((course, index) => (
                  <button className="mini-course-card" type="button" key={course.id} onClick={() => openCourse(course.id)} disabled={busy}>
                    <div className={'mini-course-art mini-course-art-' + (index % 4)}>
                      <span className="mini-art-symbol">{['✧', '⌖', '✦', '❋'][index % 4]}</span>
                      <span className="mini-art-label">КУРС {String(index + 1).padStart(2, '0')}</span>
                    </div>
                    <div className="mini-course-card-body">
                      <span className="mini-course-card-meta">{course.lesson_count} {course.lesson_count === 1 ? 'урок' : course.lesson_count >= 2 && course.lesson_count <= 4 ? 'урока' : 'уроков'}</span>
                      <h3>{course.title}</h3>
                      <p>{course.description || 'Последовательное изучение библейских тем и текстов.'}</p>
                      <span className="mini-course-open">Открыть курс <b>→</b></span>
                    </div>
                  </button>
                ))}
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
              {runProgress?.enrollment.course_id === selectedCourse.id ? (
                <button className="mini-primary-button mini-full-button" type="button" onClick={continueCourse} disabled={busy}>Продолжить курс <span>→</span></button>
              ) : (
                <button className="mini-primary-button mini-full-button" type="button" onClick={() => setView('mode')} disabled={busy}>Начать обучение <span>→</span></button>
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

        {!loading && view === 'lesson' && questionState && (
          <section className="mini-page mini-lesson-page">
            <div className="mini-lesson-progress-head">
              <div><p className="mini-overline">{lessonTitle}</p><h1>{questionState.at_end ? 'Урок пройден' : 'Вопрос ' + (activeQuestion?.number || 1)}</h1></div>
              {!questionState.at_end && <span>{questionState.progress.answered_questions} / {questionState.progress.total_questions}</span>}
            </div>
            <div className="mini-progress"><span style={{ width: progressPercent(questionState.progress.answered_questions, questionState.progress.total_questions) + '%' }} /></div>

            {questionState.at_end || !activeQuestion ? (
              <div className="mini-panel mini-finish-card">
                <div className="mini-finish-icon">✦</div>
                <p className="mini-overline">ПОСЛЕДНИЙ ШАГ</p>
                <h2>Вы ответили на все вопросы</h2>
                <p>{runProgress?.enrollment.mode === 'curator'
                  ? 'Отправьте урок координатору. Он проверит ваши ответы и при необходимости свяжется с вами.'
                  : 'Завершите урок, чтобы сохранить результат и перейти к следующему.'}</p>
                <button className="mini-primary-button mini-full-button" type="button" onClick={finishLesson} disabled={busy}>Завершить урок <span>→</span></button>
              </div>
            ) : (
              <article className="mini-panel mini-question-card">
                <div className="mini-question-label">ВОПРОС {activeQuestion.number}</div>
                <h2>{activeQuestion.prompt}</h2>

                {activeQuestion.bible_passages.length > 0 && (
                  <div className="mini-scripture-list">
                    {activeQuestion.bible_passages.map((passage, index) => (
                      <div className="mini-scripture" key={index}>
                        <span>БИБЛЕЙСКИЙ ОТРЫВОК</span>
                        <strong>{passage.book_name} {passage.chapter}:{passage.start_verse}{passage.end_verse > passage.start_verse ? '–' + passage.end_verse : ''}</strong>
                        {passage.passage_text && <p>{passage.passage_text}</p>}
                      </div>
                    ))}
                  </div>
                )}

                {activeQuestion.answer_type === 'text' ? (
                  <label className="mini-answer-field">
                    <span>Ваш ответ</span>
                    <textarea rows={5} value={answerText} placeholder="Напишите свой ответ…" onChange={(event) => { setAnswerText(event.target.value); setAnswerDirty(true); setError('') }} />
                  </label>
                ) : (
                  <div className="mini-answer-options">
                    <p>{activeQuestion.answer_type === 'multiple_choice' ? 'Выберите все подходящие варианты' : 'Выберите один вариант'}</p>
                    {activeQuestion.options.map((option) => {
                      const selected = selectedOptions.includes(option.id)
                      return (
                        <button className={'mini-answer-option' + (selected ? ' is-selected' : '')} type="button" key={option.id}
                          onClick={() => {
                            setSelectedOptions((current) => activeQuestion.answer_type === 'single_choice'
                              ? [option.id]
                              : selected ? current.filter((id) => id !== option.id) : [...current, option.id])
                            setAnswerDirty(true)
                            setError('')
                          }}>
                          <span className="mini-option-check">{selected ? '✓' : ''}</span><span>{option.text}</span>
                        </button>
                      )
                    })}
                  </div>
                )}

                {activeQuestion.latest_attempt?.review_feedback && (
                  <div className="mini-feedback-note">
                    <strong>{activeQuestion.latest_attempt.review_decision === 'needs_revision' ? 'Комментарий координатора' : 'Ответ проверен'}</strong>
                    <p>{activeQuestion.latest_attempt.review_feedback}</p>
                  </div>
                )}

                {activeQuestion.saved_answer && !answerDirty && (
                  <div className="mini-saved-note"><span>✓</span> Ответ сохранён</div>
                )}
                <button className="mini-primary-button mini-full-button" type="button" onClick={saveAnswer} disabled={busy}>
                  {busy ? 'Сохраняем…' : 'Сохранить ответ'} <span>→</span>
                </button>
                <div className="mini-question-navigation">
                  <button type="button" onClick={() => navigateQuestion('previous')} disabled={busy || !questionState.progress.previous_question_id}>← Назад</button>
                  <button type="button" onClick={() => navigateQuestion('next')} disabled={busy || answerDirty || !activeQuestion.saved_answer}>Следующий вопрос →</button>
                </div>
              </article>
            )}
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
