import { useCallback, useEffect, useMemo, useState } from 'react'

type Page = 'overview' | 'courses' | 'lessons' | 'students' | 'analytics' | 'settings'
type HealthState = 'checking' | 'connected' | 'error'
type IconName = 'grid' | 'book' | 'layers' | 'users' | 'chart' | 'settings' | 'search' | 'bell' | 'arrow' | 'chevron' | 'clock' | 'activity' | 'calendar' | 'menu' | 'x' | 'help' | 'filter'
const iconPaths: Record<IconName, string> = {
  grid: 'M3 3h7v7H3z M14 3h7v4h-7z M14 10h7v11h-7z M3 13h7v8H3z',
  book: 'M4 4.5A2.5 2.5 0 0 1 6.5 2H20v17H6.5A2.5 2.5 0 0 0 4 21.5z M4 4.5v17 M8 6h8 M8 10h7',
  layers: 'm12 3 9 5-9 5-9-5 9-5z M3 12l9 5 9-5 M3 16l9 5 9-5',
  users: 'M16 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2 M10 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8z M20 8v6 M23 11h-6',
  chart: 'M3 3v18h18 M7 14l4-4 4 3 6-7 M17 6h4v4',
  settings: 'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8z M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1-1.7 2.9-.2-.1a1.7 1.7 0 0 0-1.9.2 1.7 1.7 0 0 0-.8 1.5v.2h-3.4v-.2a1.7 1.7 0 0 0-1.2-1.6 1.7 1.7 0 0 0-1.8.4l-.2.1-2.4-2.4.1-.2a1.7 1.7 0 0 0 .2-1.9A1.7 1.7 0 0 0 5 15h-.2v-3.4H5a1.7 1.7 0 0 0 1.6-1.2 1.7 1.7 0 0 0-.4-1.8l-.1-.2 2.4-2.4.2.1a1.7 1.7 0 0 0 1.9.2A1.7 1.7 0 0 0 11.4 5v-.2h3.4V5a1.7 1.7 0 0 0 1.2 1.6 1.7 1.7 0 0 0 1.8-.4l.2-.1 2.4 2.4-.1.2a1.7 1.7 0 0 0-.2 1.9 1.7 1.7 0 0 0 1.5.8h.2v3.4h-.2a1.7 1.7 0 0 0-1.2.2z',
  search: 'm20 20-4.5-4.5 M18 10.5a7.5 7.5 0 1 1-15 0 7.5 7.5 0 0 1 15 0z',
  bell: 'M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9 M10 21h4',
  arrow: 'M7 17 17 7 M7 7h10v10', chevron: 'm9 18 6-6-6-6',
  clock: 'M12 8v4l3 2 M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0z',
  activity: 'M3 12h4l3-8 4 16 3-8h4',
  calendar: 'M8 2v4 M16 2v4 M3 9h18 M5 4h14a2 2 0 0 1 2 2v14H3V6a2 2 0 0 1 2-2z',
  menu: 'M4 6h16 M4 12h16 M4 18h16', x: 'M18 6 6 18 M6 6l12 12',
  help: 'M12 18h.01 M9.1 9a3 3 0 0 1 5.8 1c0 2-3 3-3 5 M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0z',
  filter: 'M4 6h16 M7 12h10 M10 18h4',
}
const courses = [
  { id: 'C-001', title: 'Основы изучения Библии', category: 'Основной курс', students: 324, progress: 78, status: 'Опубликован', color: 'mint', initials: 'ОИ' },
  { id: 'C-002', title: 'Евангелие от Иоанна', category: 'Изучение Писания', students: 218, progress: 64, status: 'Опубликован', color: 'lavender', initials: 'Ио' },
  { id: 'C-003', title: 'История ранней церкви', category: 'История', students: 146, progress: 42, status: 'Черновик', color: 'peach', initials: 'РЦ' },
]
const students = [
  { name: 'Анна Петрова', email: 'anna.petrova@example.com', course: 'Основы изучения Библии', progress: 86, initials: 'АП', color: 'mint' },
  { name: 'Михаил Соколов', email: 'm.sokolov@example.com', course: 'Евангелие от Иоанна', progress: 72, initials: 'МС', color: 'lavender' },
  { name: 'Елена Волкова', email: 'e.volkova@example.com', course: 'Основы изучения Библии', progress: 58, initials: 'ЕВ', color: 'peach' },
  { name: 'Даниил Смирнов', email: 'd.smirnov@example.com', course: 'История ранней церкви', progress: 34, initials: 'ДС', color: 'blue' },
]
const lessons = [
  { title: 'Как читать Библию', course: 'Основы изучения Библии', duration: '12 мин', status: 'Опубликован' },
  { title: 'Контекст и историческая эпоха', course: 'Основы изучения Библии', duration: '18 мин', status: 'Опубликован' },
  { title: 'Пролог Евангелия от Иоанна', course: 'Евангелие от Иоанна', duration: '15 мин', status: 'Черновик' },
]
const activities = [
  { initials: 'АП', color: 'mint', title: 'Анна Петрова', detail: 'завершила урок «Введение в курс»', time: '12 минут назад' },
  { initials: 'МС', color: 'lavender', title: 'Михаил Соколов', detail: 'продолжил курс «Евангелие от Иоанна»', time: '38 минут назад' },
  { initials: 'ЕВ', color: 'peach', title: 'Елена Волкова', detail: 'ответила на вопросы урока', time: '1 час назад' },
]
const weekly = [{ day: 'Пн', height: 42 }, { day: 'Вт', height: 64 }, { day: 'Ср', height: 52 }, { day: 'Чт', height: 82 }, { day: 'Пт', height: 68 }, { day: 'Сб', height: 92 }, { day: 'Вс', height: 58 }]
const navigation: { id: Page; label: string; icon: IconName }[] = [
  { id: 'overview', label: 'Обзор', icon: 'grid' }, { id: 'courses', label: 'Курсы', icon: 'book' },
  { id: 'lessons', label: 'Уроки', icon: 'layers' }, { id: 'students', label: 'Ученики', icon: 'users' },
  { id: 'analytics', label: 'Аналитика', icon: 'chart' },
]
const pageTitles: Record<Page, string> = { overview: 'Обзор', courses: 'Курсы', lessons: 'Уроки', students: 'Ученики', analytics: 'Аналитика', settings: 'Настройки' }

function Icon({ name, size = 18 }: { name: IconName; size?: number }) {
  return <svg aria-hidden="true" width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"><path d={iconPaths[name]} /></svg>
}

function App() {
  const [activePage, setActivePage] = useState<Page>('overview')
  const [healthState, setHealthState] = useState<HealthState>('checking')
  const [healthMessage, setHealthMessage] = useState('')
  const [searchTerm, setSearchTerm] = useState('')
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const checkBackend = useCallback(async () => {
    setHealthState('checking')
    setHealthMessage('')
    try {
      const response = await fetch('/health/ready', { headers: { Accept: 'application/json' }, cache: 'no-store' })
      if (!response.ok) throw new Error('HTTP ' + response.status)
      const body: unknown = await response.json()
      if (typeof body !== 'object' || body === null || !('status' in body) || body.status !== 'ok') throw new Error('Unexpected health response')
      setHealthState('connected')
    } catch {
      setHealthState('error')
      setHealthMessage('Backend пока недоступен. Интерфейс открыт в демонстрационном режиме.')
    }
  }, [])
  useEffect(() => { void checkBackend() }, [checkBackend])
  const filteredCourses = useMemo(() => {
    const query = searchTerm.trim().toLocaleLowerCase('ru-RU')
    return query ? courses.filter((course) => (course.title + ' ' + course.category + ' ' + course.status).toLocaleLowerCase('ru-RU').includes(query)) : courses
  }, [searchTerm])
  const navigate = (page: Page) => { setActivePage(page); setSidebarOpen(false) }
  const statusLabel = healthState === 'connected' ? 'Сервер доступен' : healthState === 'checking' ? 'Проверка сервера' : 'Демо-режим'

  return <div className="app-shell">
    {sidebarOpen && <button className="sidebar-backdrop" aria-label="Закрыть меню" onClick={() => setSidebarOpen(false)} />}
    <aside className={'sidebar' + (sidebarOpen ? ' sidebar--open' : '')}>
      <div className="brand"><div className="brand-mark" aria-hidden="true"><span /><span /><span /></div><div className="brand-copy"><strong>Слово</strong><span>ОБУЧАЮЩАЯ ПЛАТФОРМА</span></div><button className="icon-button sidebar-close" aria-label="Закрыть меню" onClick={() => setSidebarOpen(false)}><Icon name="x" /></button></div>
      <div className="workspace-switcher"><div className="workspace-avatar">Б</div><div className="workspace-copy"><span>Рабочее пространство</span><strong>Библейская школа</strong></div><span className="workspace-chevron"><Icon name="chevron" size={15} /></span></div>
      <nav className="primary-nav" aria-label="Основная навигация"><p className="nav-caption">МЕНЮ</p>{navigation.map((item) => <button key={item.id} className={'nav-item' + (activePage === item.id ? ' nav-item--active' : '')} aria-current={activePage === item.id ? 'page' : undefined} onClick={() => navigate(item.id)}><Icon name={item.icon} /><span>{item.label}</span>{item.id === 'students' && <span className="nav-count">12</span>}</button>)}</nav>
      <div className="sidebar-spacer" />
      <div className="help-card"><div className="help-icon"><Icon name="help" size={17} /></div><strong>Нужна помощь?</strong><p>Подсказки по работе с платформой и материалами.</p><button onClick={() => navigate('settings')}>Центр помощи <Icon name="arrow" size={14} /></button></div>
      <button className={'nav-item settings-link' + (activePage === 'settings' ? ' nav-item--active' : '')} onClick={() => navigate('settings')}><Icon name="settings" /><span>Настройки</span></button>
      <div className="sidebar-user"><div className="avatar avatar--dark">АП</div><div className="sidebar-user-copy"><strong>Администратор</strong><span>Управление платформой</span></div><span className="user-menu-dot">•••</span></div>
    </aside>
    <main className="main-area">
      <header className="topbar"><div className="topbar-left"><button className="icon-button mobile-menu" aria-label="Открыть меню" onClick={() => setSidebarOpen(true)}><Icon name="menu" /></button><div className="breadcrumbs"><span>Рабочее пространство</span><Icon name="chevron" size={14} /><strong>{pageTitles[activePage]}</strong></div></div><div className="topbar-actions"><label className="search-box"><Icon name="search" size={17} /><input value={searchTerm} onChange={(event) => setSearchTerm(event.target.value)} placeholder="Поиск по курсам..." aria-label="Поиск по курсам" /><kbd>⌘ K</kbd></label><span className="topbar-divider" /><button className="icon-button notification-button" aria-label="Уведомления"><Icon name="bell" /><span /></button><div className="topbar-user"><div className="avatar avatar--dark">АП</div><div><strong>Администратор</strong><span>Администратор</span></div></div></div></header>
      <div className="page-content">
        <div className="demo-notice"><span className="demo-dot" /> ДЕМО-ИНТЕРФЕЙС <span className="demo-notice-copy">Показатели и списки ниже — примеры оформления, не реальные данные.</span></div>
        {healthState !== 'connected' && <div className={'health-notice health-notice--' + healthState} role="status"><span className="health-dot" />{healthState === 'checking' ? 'Проверяем подключение к серверу…' : healthMessage}{healthState === 'error' && <button onClick={() => void checkBackend()}>Повторить</button>}</div>}

        {activePage === 'overview' && <>
          <section className="welcome-row"><div><p className="eyebrow">ВОСКРЕСЕНЬЕ, 25 ОКТЯБРЯ</p><h1>Добро пожаловать! <span className="wave">✦</span></h1><p className="page-subtitle">Вот что происходит в вашей обучающей платформе сегодня.</p></div><button className="primary-button" onClick={() => navigate('courses')}>Открыть курсы <Icon name="arrow" size={17} /></button></section>
          <section className="stats-grid" aria-label="Основные показатели">
            <article className="stat-card"><div className="stat-top"><span>Всего учеников</span><span className="stat-icon stat-icon--mint"><Icon name="users" size={19} /></span></div><div className="stat-value">1 248</div><div className="stat-foot"><span className="trend trend--up">↗ 12,8%</span><span>за последний месяц</span></div><div className="sparkline sparkline--mint">{Array.from({ length: 12 }, (_, i) => <i key={i} />)}</div></article>
            <article className="stat-card"><div className="stat-top"><span>Активные курсы</span><span className="stat-icon stat-icon--lavender"><Icon name="book" size={19} /></span></div><div className="stat-value">24</div><div className="stat-foot"><span className="trend trend--up">+3</span><span>за этот месяц</span></div><div className="sparkline sparkline--lavender">{Array.from({ length: 12 }, (_, i) => <i key={i} />)}</div></article>
            <article className="stat-card"><div className="stat-top"><span>Завершённые уроки</span><span className="stat-icon stat-icon--blue"><Icon name="layers" size={19} /></span></div><div className="stat-value">8 562</div><div className="stat-foot"><span className="trend trend--up">↗ 8,2%</span><span>за последний месяц</span></div><div className="sparkline sparkline--blue">{Array.from({ length: 12 }, (_, i) => <i key={i} />)}</div></article>
            <article className="stat-card"><div className="stat-top"><span>Средний прогресс</span><span className="stat-icon stat-icon--peach"><Icon name="activity" size={19} /></span></div><div className="stat-value">68,4<span className="stat-unit">%</span></div><div className="stat-foot"><span className="trend trend--up">↗ 4,6%</span><span>за последний месяц</span></div><div className="progress-track stat-progress"><span style={{ width: '68.4%' }} /></div></article>
          </section>
          <section className="content-grid">
            <article className="panel"><div className="panel-heading"><div><h2>Активность учеников</h2><p>Динамика занятий за неделю</p></div><span className="select-button"><Icon name="calendar" size={15} /> Последние 7 дней <Icon name="chevron" size={14} /></span></div><div className="chart-summary"><strong>2 846</strong><span className="trend trend--up">↗ 14,6%</span><span>активных занятий</span></div><div className="bar-chart" role="img" aria-label="Демонстрационный график активности за семь дней">{weekly.map((item) => <div className="bar-column" key={item.day}><div className="bar-track"><span style={{ height: item.height + '%' }} /></div><span className="bar-label">{item.day}</span></div>)}</div><div className="chart-legend"><span><i /> Активность учеников</span><span className="chart-legend-note">Демо-данные</span></div></article>
            <article className="panel"><div className="panel-heading"><div><h2>Популярные курсы</h2><p>По числу участников</p></div><button className="text-button" onClick={() => navigate('courses')}>Все курсы <Icon name="chevron" size={15} /></button></div><div className="popular-courses">{courses.map((course) => <div className="popular-course" key={course.id}><div className={'course-thumb course-thumb--' + course.color}>{course.initials}</div><div className="popular-course-main"><strong>{course.title}</strong><span>{course.students} учеников</span><div className="progress-track"><span style={{ width: course.progress + '%' }} /></div></div><span className="course-progress-value">{course.progress}%</span></div>)}</div></article>
          </section>
          <section className="content-grid content-grid--bottom">
            <article className="panel"><div className="panel-heading"><div><h2>Последняя активность</h2><p>Что происходит на платформе</p></div><button className="text-button" onClick={() => navigate('students')}>Все ученики <Icon name="chevron" size={15} /></button></div><div className="activity-list">{activities.map((item) => <div className="activity-item" key={item.title}><div className={'avatar avatar--' + item.color}>{item.initials}</div><div className="activity-copy"><p><strong>{item.title}</strong> {item.detail}</p><span>{item.time}</span></div><span className="activity-status" aria-label="Активность завершена">✓</span></div>)}</div></article>
            <article className="panel quick-panel"><div className="panel-heading"><div><h2>Быстрый доступ</h2><p>Основные разделы платформы</p></div></div><button className="quick-link" onClick={() => navigate('courses')}><span className="quick-icon quick-icon--mint"><Icon name="book" /></span><span><strong>Управление курсами</strong><small>Материалы и программы обучения</small></span><Icon name="chevron" size={17} /></button><button className="quick-link" onClick={() => navigate('lessons')}><span className="quick-icon quick-icon--lavender"><Icon name="layers" /></span><span><strong>Библиотека уроков</strong><small>Уроки и учебные материалы</small></span><Icon name="chevron" size={17} /></button><button className="quick-link" onClick={() => navigate('students')}><span className="quick-icon quick-icon--blue"><Icon name="users" /></span><span><strong>Список учеников</strong><small>Участники и их прогресс</small></span><Icon name="chevron" size={17} /></button></article>
          </section>
        </>}

        {activePage === 'courses' && <section className="section-page"><div className="welcome-row"><div><p className="eyebrow">ОБУЧАЮЩИЕ ПРОГРАММЫ</p><h1>Курсы</h1><p className="page-subtitle">Обзор курсов и демонстрационных показателей обучения.</p></div><span className="soft-pill">{courses.length} примера</span></div><div className="panel table-panel"><div className="panel-heading"><div><h2>Все курсы</h2><p>Список для демонстрации будущего интерфейса управления</p></div><span className="soft-pill"><Icon name="filter" size={14} /> Все статусы</span></div><div className="table-scroll"><table><thead><tr><th>Название курса</th><th>Участники</th><th>Прогресс</th><th>Статус</th><th /></tr></thead><tbody>{filteredCourses.map((course) => <tr key={course.id}><td><div className="table-course"><div className={'course-thumb course-thumb--' + course.color}>{course.initials}</div><div><strong>{course.title}</strong><span>{course.category}</span></div></div></td><td>{course.students}</td><td><div className="table-progress"><div className="progress-track"><span style={{ width: course.progress + '%' }} /></div><span>{course.progress}%</span></div></td><td><span className={'status-pill' + (course.status === 'Черновик' ? ' status-pill--draft' : '')}><i />{course.status}</span></td><td><button className="row-action" aria-label={'Открыть ' + course.title} onClick={() => navigate('lessons')}><Icon name="chevron" size={17} /></button></td></tr>)}{filteredCourses.length === 0 && <tr><td colSpan={5} className="empty-state">По запросу «{searchTerm}» ничего не найдено.</td></tr>}</tbody></table></div></div></section>}

        {activePage === 'lessons' && <section className="section-page"><div className="welcome-row"><div><p className="eyebrow">УЧЕБНЫЕ МАТЕРИАЛЫ</p><h1>Уроки</h1><p className="page-subtitle">Пример структуры библиотеки уроков.</p></div><span className="soft-pill">{lessons.length} примера</span></div><div className="panel table-panel"><div className="panel-heading"><div><h2>Библиотека уроков</h2><p>Уроки сгруппированы по учебным курсам</p></div></div><div className="lesson-list">{lessons.filter((lesson) => (lesson.title + ' ' + lesson.course).toLocaleLowerCase('ru-RU').includes(searchTerm.trim().toLocaleLowerCase('ru-RU'))).map((lesson) => <div className="lesson-row" key={lesson.title}><div className="lesson-icon"><Icon name="book" /></div><div className="lesson-copy"><strong>{lesson.title}</strong><span>{lesson.course}</span></div><span className="lesson-duration"><Icon name="clock" size={14} />{lesson.duration}</span><span className={'status-pill' + (lesson.status === 'Черновик' ? ' status-pill--draft' : '')}><i />{lesson.status}</span></div>)}</div></div></section>}

        {activePage === 'students' && <section className="section-page"><div className="welcome-row"><div><p className="eyebrow">СООБЩЕСТВО УЧЕНИКОВ</p><h1>Ученики</h1><p className="page-subtitle">Пример списка участников и их учебного прогресса.</p></div><span className="soft-pill">{students.length} примера</span></div><div className="panel table-panel"><div className="panel-heading"><div><h2>Все ученики</h2><p>Демонстрационные профили и прогресс прохождения курсов</p></div></div><div className="table-scroll"><table><thead><tr><th>Ученик</th><th>Курс</th><th>Прогресс</th><th>Статус</th></tr></thead><tbody>{students.filter((student) => (student.name + ' ' + student.email + ' ' + student.course).toLocaleLowerCase('ru-RU').includes(searchTerm.trim().toLocaleLowerCase('ru-RU'))).map((student) => <tr key={student.email}><td><div className="table-course"><div className={'avatar avatar--' + student.color}>{student.initials}</div><div><strong>{student.name}</strong><span>{student.email}</span></div></div></td><td>{student.course}</td><td><div className="table-progress"><div className="progress-track"><span style={{ width: student.progress + '%' }} /></div><span>{student.progress}%</span></div></td><td><span className="status-pill"><i />Активен</span></td></tr>)}</tbody></table></div></div></section>}

        {activePage === 'analytics' && <section className="section-page"><div className="welcome-row"><div><p className="eyebrow">ОТЧЁТЫ И ПОКАЗАТЕЛИ</p><h1>Аналитика</h1><p className="page-subtitle">Пример визуализации активности и прогресса обучения.</p></div><span className="soft-pill"><Icon name="calendar" size={14} /> 7 дней</span></div><div className="stats-grid stats-grid--analytics"><article className="stat-card"><div className="stat-top"><span>Занятия за неделю</span><span className="stat-icon stat-icon--mint"><Icon name="activity" /></span></div><div className="stat-value">2 846</div><div className="stat-foot"><span className="trend trend--up">↗ 14,6%</span><span>демо-данные</span></div></article><article className="stat-card"><div className="stat-top"><span>Завершение курсов</span><span className="stat-icon stat-icon--lavender"><Icon name="chart" /></span></div><div className="stat-value">68,4%</div><div className="stat-foot"><span className="trend trend--up">↗ 4,6%</span><span>демо-данные</span></div></article></div><article className="panel analytics-chart"><div className="panel-heading"><div><h2>Активность по дням</h2><p>Демонстрационные данные за неделю</p></div></div><div className="bar-chart bar-chart--large">{weekly.map((item) => <div className="bar-column" key={item.day}><div className="bar-track"><span style={{ height: item.height + '%' }} /></div><span className="bar-label">{item.day}</span></div>)}</div></article></section>}

        {activePage === 'settings' && <section className="section-page"><div className="welcome-row"><div><p className="eyebrow">РАБОЧЕЕ ПРОСТРАНСТВО</p><h1>Настройки</h1><p className="page-subtitle">Общие сведения о текущей демонстрационной сборке.</p></div></div><div className="panel settings-panel"><div className="settings-mark"><Icon name="settings" size={24} /></div><h2>Основа платформы</h2><p>Интерфейс подготовлен к дальнейшему подключению авторизации, API и реальных данных. Сейчас элементы управления учебным содержимым работают в демонстрационном режиме.</p><div className="settings-row"><span>Backend API</span><strong className={'connection-label connection-label--' + healthState}>{healthState === 'connected' ? 'Доступен' : healthState === 'checking' ? 'Проверяется' : 'Недоступен'}</strong></div><div className="settings-row"><span>Режим интерфейса</span><strong>Демонстрационный</strong></div><div className="settings-row"><span>Подключение базы данных</span><strong>Через backend</strong></div><button className="secondary-button" onClick={() => void checkBackend()}>Проверить подключение <Icon name="arrow" size={16} /></button></div></section>}

        <footer className="page-footer"><span>© 2026 Слово · Обучающая платформа</span><span><i className={'footer-health footer-health--' + healthState} />{statusLabel}</span></footer>
      </div>
    </main>
  </div>
}
export default App

