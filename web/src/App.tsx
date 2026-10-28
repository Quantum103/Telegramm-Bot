import { useCallback, useEffect, useMemo, useState, type FormEvent } from 'react'
import {
  getStaffSession, listCourseLessons, listStaffCourses, loginStaff, logoutStaff,
  type Course, type Lesson, type StaffIdentity,
} from './api'
import DocxImportPage from './DocxImportPage'
import StudentsPage from './StudentsPage'
import StaffManagementPage from './StaffManagementPage'
import PasswordChangePage from './PasswordChangePage'
import './catalog.css'
import './auth.css'

type Page = 'overview' | 'courses' | 'lessons' | 'docx-import' | 'students' | 'staff'
type AuthState = 'checking' | 'signed-out' | 'signed-in'
type IconName = 'grid' | 'book' | 'layers' | 'file-up' | 'chevron' | 'menu' | 'x' | 'logout' | 'search' | 'refresh' | 'users' | 'staff'
const iconPaths: Record<IconName, string> = {
  grid: 'M3 3h7v7H3z M14 3h7v4h-7z M14 10h7v11h-7z M3 13h7v8H3z',
  book: 'M4 4.5A2.5 2.5 0 0 1 6.5 2H20v17H6.5A2.5 2.5 0 0 0 4 21.5z M4 4.5v17 M8 6h8 M8 10h7',
  layers: 'm12 3 9 5-9 5-9-5 9-5z M3 12l9 5 9-5 M3 16l9 5 9-5',
  'file-up': 'M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z M14 2v6h6 M12 18v-7 M9 14l3-3 3 3',
  chevron: 'm9 18 6-6-6-6', menu: 'M4 6h16 M4 12h16 M4 18h16',
  x: 'M18 6 6 18 M6 6l12 12', logout: 'M10 17l5-5-5-5 M15 12H3 M12 3h6a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-6',
  search: 'm20 20-4.5-4.5 M18 10.5a7.5 7.5 0 1 1-15 0 7.5 7.5 0 0 1 15 0z',
  refresh: 'M20 7v5h-5 M4 17v-5h5 M5.6 9a7 7 0 0 1 12-2L20 12 M4 12l2.4 5a7 7 0 0 0 12-2',
  users: 'M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2 M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8 M22 21v-2a4 4 0 0 0-3-3.87 M16 3.13a4 4 0 0 1 0 7.75',
  staff: 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8 M4 21v-2a8 8 0 0 1 16 0v2 M19 8h3 M20.5 6.5v3',
}
const navigation: { id: Page; label: string; icon: IconName }[] = [
  { id: 'overview', label: 'Обзор', icon: 'grid' },
  { id: 'courses', label: 'Курсы', icon: 'book' },
  { id: 'lessons', label: 'Уроки', icon: 'layers' },
  { id: 'docx-import', label: 'Импорт DOCX', icon: 'file-up' },
  { id: 'students', label: 'Ученики', icon: 'users' },
  { id: 'staff', label: 'Сотрудники', icon: 'staff' },
]
const pageTitles: Record<Page, string> = {
  overview: 'Обзор', courses: 'Курсы', lessons: 'Уроки', 'docx-import': 'Импорт DOCX',
  students: 'Ученики', staff: 'Сотрудники',
}

function Icon({ name, size = 18 }: { name: IconName; size?: number }) {
  return <svg aria-hidden="true" width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"><path d={iconPaths[name]} /></svg>
}

function LoginPage({
  error, email, password, busy, onEmailChange, onPasswordChange, onSubmit,
}: {
  error: string
  email: string
  password: string
  busy: boolean
  onEmailChange: (value: string) => void
  onPasswordChange: (value: string) => void
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
}) {
  return <main className="auth-shell">
    <section className="auth-card" aria-labelledby="auth-heading">
      <div className="auth-brand">
        <div className="auth-brand-mark" aria-hidden="true"><span /><span /><span /></div>
        <div><strong>Слово</strong><span>ОБУЧАЮЩАЯ ПЛАТФОРМА</span></div>
      </div>
      <p className="eyebrow">ЗАКРЫТАЯ РАБОЧАЯ ОБЛАСТЬ</p>
      <h1 id="auth-heading">Вход в платформу</h1>
      <p className="auth-subtitle">Войдите под учётной записью администратора или сотрудника, чтобы открыть панель управления.</p>
      {error && <p className="auth-error" role="alert">{error}</p>}
      <form className="auth-form" onSubmit={onSubmit}>
        <label className="docx-field"><span>Email</span><input className="docx-input" type="email" autoComplete="username" required maxLength={254} value={email} onChange={(event) => onEmailChange(event.target.value)} /></label>
        <label className="docx-field"><span>Пароль</span><input className="docx-input" type="password" autoComplete="current-password" required maxLength={1024} value={password} onChange={(event) => onPasswordChange(event.target.value)} /></label>
        <button className="primary-button auth-submit" type="submit" disabled={busy}>{busy ? 'Входим…' : 'Войти'}</button>
      </form>
      <p className="auth-footnote">Новая учётная запись создаётся при первом запуске backend, если активного администратора ещё нет. Публичной регистрации нет.</p>
    </section>
  </main>
}

function App() {
  const [authState, setAuthState] = useState<AuthState>('checking')
  const [staff, setStaff] = useState<StaffIdentity | null>(null)
  const [authError, setAuthError] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [loginBusy, setLoginBusy] = useState(false)
  const [logoutBusy, setLogoutBusy] = useState(false)
  const [logoutError, setLogoutError] = useState('')
  const [activePage, setActivePage] = useState<Page>('overview')
  const [searchTerm, setSearchTerm] = useState('')
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const [courses, setCourses] = useState<Course[]>([])
  const [coursesState, setCoursesState] = useState<'loading' | 'loaded' | 'error'>('loading')
  const [coursesError, setCoursesError] = useState('')
  const [coursesRevision, setCoursesRevision] = useState(0)
  const [selectedCourseID, setSelectedCourseID] = useState<number | null>(null)
  const [lessons, setLessons] = useState<Lesson[]>([])
  const [lessonsState, setLessonsState] = useState<'idle' | 'loading' | 'loaded' | 'error'>('idle')
  const [lessonsError, setLessonsError] = useState('')

  const onSessionExpired = useCallback(() => {
    setStaff(null)
    setAuthError('Сессия завершена. Войдите в систему ещё раз.')
    setAuthState('signed-out')
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    void getStaffSession(controller.signal).then((session) => {
      if (controller.signal.aborted) return
      if (session) {
        setStaff(session.staff)
        setAuthState('signed-in')
      } else {
        setAuthState('signed-out')
      }
    }).catch((error: unknown) => {
      if (controller.signal.aborted) return
      setAuthError(error instanceof Error ? error.message : 'Не удалось проверить сессию.')
      setAuthState('signed-out')
    })
    return () => controller.abort()
  }, [])

  useEffect(() => {
    if (authState !== 'signed-in' || !staff || staff.must_change_password) return
    const controller = new AbortController()
    setCoursesState('loading')
    setCoursesError('')
    void listStaffCourses(controller.signal).then((items) => {
      if (controller.signal.aborted) return
      setCourses(items)
      setCoursesState('loaded')
      setSelectedCourseID((current) => current !== null && items.some((item) => item.id === current)
        ? current : items.find((item) => item.status === 'published')?.id ?? null)
    }).catch((error: unknown) => {
      if (controller.signal.aborted) return
      const message = error instanceof Error ? error.message : 'Не удалось загрузить курсы.'
      setCoursesError(message)
      setCoursesState('error')
      if (message.includes('Сессия завершена')) {
        setStaff(null)
        setAuthError(message)
        setAuthState('signed-out')
      }
    })
    return () => controller.abort()
  }, [authState, staff, coursesRevision])

  useEffect(() => {
    if (authState !== 'signed-in' || activePage !== 'lessons' || selectedCourseID === null) return
    const selected = courses.find((course) => course.id === selectedCourseID)
    if (!selected || selected.status !== 'published') {
      setLessons([])
      setLessonsState('loaded')
      setLessonsError('')
      return
    }
    const controller = new AbortController()
    setLessonsState('loading')
    setLessonsError('')
    void listCourseLessons(selectedCourseID, controller.signal).then((items) => {
      if (controller.signal.aborted) return
      setLessons(items)
      setLessonsState('loaded')
    }).catch((error: unknown) => {
      if (controller.signal.aborted) return
      const message = error instanceof Error ? error.message : 'Не удалось загрузить уроки.'
      setLessonsError(message)
      setLessonsState('error')
    })
    return () => controller.abort()
  }, [authState, activePage, selectedCourseID, courses])

  async function handleLogin(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setAuthError('')
    setLoginBusy(true)
    try {
      const identity = await loginStaff(email, password)
      setStaff(identity)
      setPassword('')
      setAuthState('signed-in')
    } catch (error: unknown) {
      setAuthError(error instanceof Error ? error.message : 'Не удалось войти в систему.')
    } finally {
      setLoginBusy(false)
    }
  }

  async function handleLogout() {
    setLogoutError('')
    setLogoutBusy(true)
    try {
      await logoutStaff()
      setStaff(null)
      setPassword('')
      setAuthError('')
      setLogoutError('')
      setAuthState('signed-out')
      setCourses([])
      setLessons([])
      setActivePage('overview')
    } catch (error: unknown) {
      setLogoutError(error instanceof Error ? error.message : 'Не удалось завершить сессию.')
    } finally {
      setLogoutBusy(false)
    }
  }

  const filteredCourses = useMemo(() => {
    const query = searchTerm.trim().toLocaleLowerCase('ru-RU')
    return query ? courses.filter((course) => (course.title + ' ' + course.slug + ' ' + (course.description ?? '')).toLocaleLowerCase('ru-RU').includes(query)) : courses
  }, [courses, searchTerm])
  const filteredLessons = useMemo(() => {
    const query = searchTerm.trim().toLocaleLowerCase('ru-RU')
    return query ? lessons.filter((lesson) => (lesson.title + ' ' + (lesson.description ?? '')).toLocaleLowerCase('ru-RU').includes(query)) : lessons
  }, [lessons, searchTerm])
  const publishedCourses = courses.filter((course) => course.status === 'published')
  const draftCourses = courses.filter((course) => course.status === 'draft')
  const navigate = (page: Page) => { setActivePage(page); setSidebarOpen(false); setSearchTerm('') }

  if (authState === 'checking') {
    return <main className="auth-shell"><div className="auth-loading" role="status">Проверяем сессию…</div></main>
  }
  if (authState === 'signed-in' && staff?.must_change_password) {
    return <PasswordChangePage staff={staff} onComplete={() => {
      setStaff(null)
      setPassword('')
      setAuthError('Пароль изменён. Войдите с новым паролем.')
      setAuthState('signed-out')
    }} />
  }
  if (authState === 'signed-out' || !staff) {
    return <LoginPage error={authError} email={email} password={password} busy={loginBusy}
      onEmailChange={setEmail} onPasswordChange={setPassword} onSubmit={handleLogin} />
  }

  return <div className="app-shell">
    {sidebarOpen && <button className="sidebar-backdrop" aria-label="Закрыть меню" onClick={() => setSidebarOpen(false)} />}
    <aside className={'sidebar' + (sidebarOpen ? ' sidebar--open' : '')}>
      <div className="brand"><div className="brand-mark" aria-hidden="true"><span /><span /><span /></div><div className="brand-copy"><strong>Слово</strong><span>ОБУЧАЮЩАЯ ПЛАТФОРМА</span></div><button className="icon-button sidebar-close" aria-label="Закрыть меню" onClick={() => setSidebarOpen(false)}><Icon name="x" /></button></div>
      <div className="workspace-switcher"><div className="workspace-avatar">С</div><div className="workspace-copy"><span>Рабочее пространство</span><strong>Библейская платформа</strong></div><span className="workspace-chevron"><Icon name="chevron" size={15} /></span></div>
      <nav className="primary-nav" aria-label="Основная навигация"><p className="nav-caption">МЕНЮ</p>{navigation.filter((item) => item.id !== 'staff' || staff.roles.includes('administrator')).map((item) => <button key={item.id} className={'nav-item' + (activePage === item.id ? ' nav-item--active' : '')} aria-current={activePage === item.id ? 'page' : undefined} onClick={() => navigate(item.id)}><Icon name={item.icon} /><span>{item.label}</span></button>)}</nav>
      <div className="sidebar-spacer" />
      <div className="help-card"><strong>Работа с уроками</strong><p>Создайте черновой курс и импортируйте вопросы из DOCX.</p><button onClick={() => navigate('docx-import')}>Импортировать DOCX <Icon name="chevron" size={14} /></button></div>
      <div className="sidebar-user"><div className="avatar avatar--dark">{(staff.display_name || staff.email).trim().slice(0, 1).toLocaleUpperCase('ru-RU')}</div><div className="sidebar-user-copy"><strong>{staff.display_name || staff.email}</strong><span>{staff.email}</span></div></div>
    </aside>

    <main className="main-area">
      <header className="topbar">
        <div className="topbar-left"><button className="icon-button mobile-menu" aria-label="Открыть меню" onClick={() => setSidebarOpen(true)}><Icon name="menu" /></button><div className="breadcrumbs"><span>Рабочее пространство</span><Icon name="chevron" size={14} /><strong>{pageTitles[activePage]}</strong></div></div>
        <div className="topbar-actions">
          <label className="search-box"><Icon name="search" size={17} /><input value={searchTerm} onChange={(event) => setSearchTerm(event.target.value)} placeholder="Поиск по курсам и урокам" aria-label="Поиск по курсам и урокам" /></label>
          <span className="topbar-divider" />
          <div className="topbar-user"><div className="avatar avatar--dark">{(staff.display_name || staff.email).trim().slice(0, 1).toLocaleUpperCase('ru-RU')}</div><div><strong>{staff.display_name || staff.email}</strong><span>{staff.email}</span></div></div>
          <button className="secondary-button auth-logout" type="button" onClick={() => void handleLogout()} disabled={logoutBusy}><Icon name="logout" size={15} />{logoutBusy ? 'Выходим…' : 'Выйти'}</button>
        </div>
      </header>
      <div className="page-content">
        {logoutError && <p className="auth-inline-error" role="alert">{logoutError}</p>}
        {coursesState === 'error' && <div className="health-notice health-notice--error" role="alert"><span className="health-dot" />{coursesError}<button onClick={() => setCoursesRevision((revision) => revision + 1)}><Icon name="refresh" size={14} /> Повторить</button></div>}

        {activePage === 'overview' && <>
          <section className="dashboard-hero" aria-labelledby="dashboard-hero-title">
            <div className="dashboard-hero-copy">
              <p className="dashboard-hero-kicker"><span aria-hidden="true" /> СЛОВО · РАБОЧЕЕ ПРОСТРАНСТВО</p>
              <h1 id="dashboard-hero-title">Помогаем слову находить <span>место в жизни.</span></h1>
              <p className="dashboard-hero-subtitle">Создавайте библейские уроки, управляйте курсами и сопровождайте учеников — без лишних шагов.</p>
              <div className="dashboard-hero-actions">
                <button className="dashboard-hero-button" onClick={() => navigate('docx-import')}><Icon name="file-up" size={16} /> Импортировать урок <Icon name="chevron" size={15} /></button>
                <span className="dashboard-hero-count"><Icon name="book" size={15} />{coursesState === 'loading' ? 'Загружаем библиотеку…' : coursesState === 'error' ? 'Каталог недоступен' : `${courses.length} курсов в каталоге`}</span>
              </div>
              <p className="dashboard-hero-greeting">Добро пожаловать, {staff.display_name || staff.email}</p>
            </div>
            <div className="dashboard-hero-art" aria-hidden="true">
              <div className="hero-art-orbit hero-art-orbit--one" />
              <div className="hero-art-orbit hero-art-orbit--two" />
              <div className="hero-art-glow" />
              <div className="hero-art-book"><Icon name="book" size={54} /><span className="hero-art-book-line" /><span className="hero-art-book-dot" /></div>
              <div className="hero-art-verse"><span>ПСАЛОМ 118:105</span><strong>Слово — свет для пути.</strong><i /></div>
              <span className="hero-art-spark hero-art-spark--one" />
              <span className="hero-art-spark hero-art-spark--two" />
            </div>
          </section>
          <section className="stats-grid" aria-label="Курсы в базе данных">
            <article className="stat-card"><div className="stat-top"><span>Всего курсов</span><span className="stat-icon stat-icon--mint"><Icon name="book" size={19} /></span></div><div className="stat-value">{coursesState === 'loading' ? '—' : courses.length}</div><div className="stat-foot"><span>Записи в базе данных</span></div></article>
            <article className="stat-card"><div className="stat-top"><span>Опубликовано</span><span className="stat-icon stat-icon--lavender"><Icon name="layers" size={19} /></span></div><div className="stat-value">{coursesState === 'loading' ? '—' : publishedCourses.length}</div><div className="stat-foot"><span>Доступны ученикам</span></div></article>
            <article className="stat-card"><div className="stat-top"><span>Черновики</span><span className="stat-icon stat-icon--blue"><Icon name="file-up" size={19} /></span></div><div className="stat-value">{coursesState === 'loading' ? '—' : draftCourses.length}</div><div className="stat-foot"><span>Можно продолжить редактировать</span></div></article>
          </section>
          <section className="panel dashboard-courses">
            <div className="panel-heading"><div><h2>Курсы</h2><p>Данные с сервера, без демонстрационных записей.</p></div><button className="text-button" onClick={() => navigate('courses')}>Все курсы <Icon name="chevron" size={15} /></button></div>
            {coursesState === 'loading' && <p className="catalog-message" role="status">Загружаем курсы…</p>}
            {coursesState === 'loaded' && courses.length === 0 && <div className="empty-panel"><p>В базе пока нет курсов.</p><button className="secondary-button" onClick={() => navigate('docx-import')}>Создать черновой курс</button></div>}
            {coursesState === 'loaded' && courses.length > 0 && <div className="dashboard-course-list">{courses.slice(0, 5).map((course) => <button className="dashboard-course-row" key={course.id} onClick={() => { setSelectedCourseID(course.id); navigate(course.status === 'published' ? 'lessons' : 'courses') }}><span className="course-thumb course-thumb--mint">{course.title.trim().slice(0, 2).toLocaleUpperCase('ru-RU')}</span><span className="dashboard-course-main"><strong>{course.title}</strong><small>{course.lesson_count} уроков · {course.slug}</small></span><span className={'course-state course-state--' + course.status}>{course.status === 'published' ? 'Опубликован' : course.status === 'draft' ? 'Черновик' : 'Архив'}</span><Icon name="chevron" size={16} /></button>)}</div>}
          </section>
        </>}

        {activePage === 'courses' && <section className="section-page section-page--courses"><div className="welcome-row"><div><p className="eyebrow">УПРАВЛЕНИЕ КОНТЕНТОМ</p><h1>Курсы</h1><p className="page-subtitle">Все курсы, сохранённые в базе данных.</p></div><button className="primary-button" onClick={() => navigate('docx-import')}><Icon name="file-up" size={16} /> Новый черновой курс</button></div><div className="panel table-panel"><div className="panel-heading"><div><h2>Все курсы</h2><p>{courses.length} записей в базе</p></div><button className="icon-button" aria-label="Обновить список" onClick={() => setCoursesRevision((revision) => revision + 1)}><Icon name="refresh" /></button></div>{coursesState === 'loading' && <p className="catalog-message" role="status">Загружаем курсы…</p>}{coursesState === 'loaded' && <div className="table-scroll"><table><thead><tr><th>Название</th><th>Статус</th><th>Количество уроков</th><th /></tr></thead><tbody>{filteredCourses.map((course) => <tr key={course.id}><td><div className="table-course"><div className="course-thumb course-thumb--mint">{course.title.trim().slice(0, 2).toLocaleUpperCase('ru-RU')}</div><div><strong>{course.title}</strong><span>{course.description || course.slug}</span></div></div></td><td><span className={'course-state course-state--' + course.status}>{course.status === 'published' ? 'Опубликован' : course.status === 'draft' ? 'Черновик' : 'Архив'}</span></td><td>{course.lesson_count}</td><td><button className="row-action" aria-label={'Посмотреть уроки курса ' + course.title} disabled={course.status !== 'published'} onClick={() => { setSelectedCourseID(course.id); navigate('lessons') }}><Icon name="chevron" size={17} /></button></td></tr>)}{filteredCourses.length === 0 && <tr><td colSpan={4} className="empty-state">{searchTerm.trim() ? 'По запросу ничего не найдено.' : 'В базе пока нет курсов.'}</td></tr>}</tbody></table></div>}</div></section>}

        {activePage === 'lessons' && <section className="section-page section-page--lessons"><div className="welcome-row"><div><p className="eyebrow">УЧЕБНЫЕ МАТЕРИАЛЫ</p><h1>Уроки</h1><p className="page-subtitle">Опубликованные уроки выбранного курса.</p></div><span className="soft-pill">{lessonsState === 'loaded' ? lessons.length : '—'} уроков</span></div><div className="panel table-panel"><div className="panel-heading"><div><h2>Библиотека уроков</h2><p>Выберите опубликованный курс из базы данных.</p></div><label className="course-select-label"><span>Курс</span><select value={selectedCourseID ?? ''} onChange={(event) => setSelectedCourseID(event.target.value ? Number(event.target.value) : null)} disabled={publishedCourses.length === 0}><option value="">Выберите курс</option>{publishedCourses.map((course) => <option key={course.id} value={course.id}>{course.title}</option>)}</select></label></div>{publishedCourses.length === 0 && coursesState === 'loaded' && <p className="catalog-message">Пока нет опубликованных курсов. Черновые уроки можно импортировать в разделе «Импорт DOCX».</p>}{lessonsState === 'loading' && <p className="catalog-message" role="status">Загружаем уроки…</p>}{lessonsState === 'error' && <div className="catalog-error" role="alert"><span>{lessonsError}</span><button className="secondary-button" onClick={() => setCoursesRevision((revision) => revision + 1)}>Обновить данные</button></div>}{lessonsState === 'loaded' && selectedCourseID !== null && <div className="lesson-list">{filteredLessons.map((lesson) => <div className="lesson-row" key={lesson.id}><div className="lesson-icon"><Icon name="book" /></div><div className="lesson-copy"><strong>{lesson.title}</strong><span>{lesson.description || 'Опубликованный урок'}</span></div><span className="lesson-duration"><Icon name="layers" size={14} />{lesson.question_count} вопр.</span><span className="status-pill"><i />Опубликован</span></div>)}{filteredLessons.length === 0 && <p className="catalog-message">{searchTerm.trim() ? 'По запросу ничего не найдено.' : 'В выбранном курсе пока нет опубликованных уроков.'}</p>}</div>}</div></section>}

        {activePage === 'docx-import' && <DocxImportPage staff={staff} onSessionExpired={onSessionExpired} />}
        {activePage === 'students' && <StudentsPage staff={staff} onSessionExpired={onSessionExpired} />}
        {activePage === 'staff' && staff.roles.includes('administrator') && <StaffManagementPage staff={staff} onSessionExpired={onSessionExpired} />}

        <footer className="page-footer"><span>© 2026 Слово · Обучающая платформа</span><span>Вход выполнен: {staff.email}</span></footer>
      </div>
    </main>
    <nav className="mobile-nav" aria-label="Быстрая навигация">
      {navigation.filter((item) => item.id === 'overview' || item.id === 'courses' || item.id === 'students').map((item) => (
        <button key={item.id} className={'mobile-nav-item' + (activePage === item.id ? ' mobile-nav-item--active' : '')}
          aria-current={activePage === item.id ? 'page' : undefined} onClick={() => navigate(item.id)}>
          <Icon name={item.icon} size={19} /><span>{item.label}</span>
        </button>
      ))}
      <button className={'mobile-nav-item' + ((['overview', 'courses', 'students'] as Page[]).includes(activePage) ? '' : ' mobile-nav-item--active')}
        type="button" aria-label="Открыть все разделы" aria-expanded={sidebarOpen} onClick={() => setSidebarOpen(true)}>
        <Icon name="menu" size={19} /><span>Ещё</span>
      </button>
    </nav>
  </div>
}
export default App
