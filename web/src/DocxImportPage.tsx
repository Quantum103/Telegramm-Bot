import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import {
  createStaffDraftCourse, importDOCXToDraft, listStaffCourses, previewDOCX,
  type Course, type DOCXPreview, type ImportedDraftLesson, type StaffIdentity,
} from './api'
import './docx-import.css'

type RequestState = 'idle' | 'loading' | 'loaded' | 'error'
const maxDOCXBytes = 8 * 1024 * 1024
const previewLimit = 12_000
const errorText = (error: unknown, fallback: string) => error instanceof Error ? error.message : fallback

export default function DocxImportPage({ staff, onSessionExpired }: { staff: StaffIdentity; onSessionExpired: () => void }) {

  const [courses, setCourses] = useState<Course[]>([])
  const [coursesState, setCoursesState] = useState<RequestState>('idle')
  const [coursesError, setCoursesError] = useState('')
  const [coursesRevision, setCoursesRevision] = useState(0)
  const draftCourses = useMemo(() => courses.filter((item) => item.status === 'draft'), [courses])
  const [selectedCourseID, setSelectedCourseID] = useState<number | null>(null)

  const [courseTitle, setCourseTitle] = useState('')
  const [courseSlug, setCourseSlug] = useState('')
  const [createCourseBusy, setCreateCourseBusy] = useState(false)
  const [createCourseMessage, setCreateCourseMessage] = useState('')

  const [file, setFile] = useState<File | null>(null)
  const [preview, setPreview] = useState<DOCXPreview | null>(null)
  const [previewedFile, setPreviewedFile] = useState<File | null>(null)
  const [previewState, setPreviewState] = useState<RequestState>('idle')
  const [previewError, setPreviewError] = useState('')
  const [importBusy, setImportBusy] = useState(false)
  const [importError, setImportError] = useState('')
  const [importedLesson, setImportedLesson] = useState<ImportedDraftLesson | null>(null)
  const fileInput = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (!staff) return
    const controller = new AbortController()
    setCoursesState('loading')
    setCoursesError('')
    void listStaffCourses(controller.signal).then((items) => {
      if (controller.signal.aborted) return
      setCourses(items)
      setCoursesState('loaded')
      setSelectedCourseID((current) =>
        current !== null && items.some((item) => item.id === current && item.status === 'draft')
          ? current
          : items.find((item) => item.status === 'draft')?.id ?? null,
      )
    }).catch((error: unknown) => {
      if (controller.signal.aborted) return
      const message = errorText(error, 'Не удалось загрузить курсы для импорта.')
      setCoursesError(message)
      setCoursesState('error')
      if (message.includes('Сессия завершена')) onSessionExpired()
    })
    return () => controller.abort()
  }, [staff, coursesRevision, onSessionExpired])

  function chooseFile(next: File | null) {
    setFile(next)
    setPreview(null)
    setPreviewedFile(null)
    setPreviewState('idle')
    setPreviewError('')
    setImportError('')
    setImportedLesson(null)
  }

  function validateFile(candidate: File): string | null {
    if (!candidate.name.toLocaleLowerCase('en-US').endsWith('.docx')) return 'Выберите документ с расширением .docx.'
    if (candidate.size === 0) return 'Выбранный файл пуст.'
    if (candidate.size > maxDOCXBytes) return 'Размер файла не должен превышать 8 МБ.'
    return null
  }

  async function handlePreview() {
    if (!file) return
    const invalid = validateFile(file)
    if (invalid) {
      setPreviewError(invalid)
      setPreviewState('error')
      return
    }
    setPreviewState('loading')
    setPreviewError('')
    setImportError('')
    setPreview(null)
    setPreviewedFile(null)
    try {
      const result = await previewDOCX(file)
      setPreview(result)
      setPreviewedFile(file)
      setPreviewState('loaded')
    } catch (error: unknown) {
      setPreviewError(errorText(error, 'Не удалось проверить документ.'))
      setPreviewState('error')
    }
  }

  async function handleImport() {
    if (!file || selectedCourseID === null || previewedFile !== file) return
    const invalid = validateFile(file)
    if (invalid) {
      setImportError(invalid)
      return
    }
    setImportError('')
    setImportBusy(true)
    try {
      const result = await importDOCXToDraft(selectedCourseID, file)
      setImportedLesson(result)
      setFile(null)
      setPreview(null)
      setPreviewedFile(null)
      setPreviewState('idle')
      if (fileInput.current) fileInput.current.value = ''
      setCoursesRevision((revision) => revision + 1)
    } catch (error: unknown) {
      setImportError(errorText(error, 'Не удалось импортировать урок.'))
    } finally {
      setImportBusy(false)
    }
  }

  async function handleCreateCourse(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setCreateCourseMessage('')
    const title = courseTitle.trim()
    const slug = courseSlug.trim()
    if (!title || title.length > 200) {
      setCreateCourseMessage('Название курса обязательно и не должно превышать 200 символов.')
      return
    }
    if (!/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(slug) || slug.length > 100) {
      setCreateCourseMessage('Slug: латинские строчные буквы и цифры, дефис между частями; максимум 100 символов.')
      return
    }
    setCreateCourseBusy(true)
    try {
      const created = await createStaffDraftCourse(title, slug)
      setCourses((items) => [created, ...items.filter((item) => item.id !== created.id)])
      setSelectedCourseID(created.id)
      setCourseTitle('')
      setCourseSlug('')
      setCreateCourseMessage('Черновой курс создан.')
    } catch (error: unknown) {
      setCreateCourseMessage(errorText(error, 'Не удалось создать курс.'))
    } finally {
      setCreateCourseBusy(false)
    }
  }

  const previewText = preview?.text ?? ''
  const visiblePreview = previewText.slice(0, previewLimit)
  const previewTruncated = previewText.length > previewLimit

  return <section className="section-page docx-import-page">
    <div className="welcome-row">
      <div><p className="eyebrow">АДМИНИСТРИРОВАНИЕ</p><h1>Импорт DOCX</h1><p className="page-subtitle">Проверьте извлечённый текст и сохраните урок в виде черновика.</p></div>
    </div>
    <div className="docx-session-line"><span className="docx-session-dot" /><span>Вход выполнен: <strong>{staff.display_name || staff.email}</strong> ({staff.email}). Для импорта нужна роль администратора.</span></div>

    {coursesState === 'error' && <div className="docx-message docx-message--error" role="alert"><span>{coursesError}</span><button className="secondary-button" type="button" onClick={() => setCoursesRevision((revision) => revision + 1)}>Повторить загрузку курсов</button></div>}

    {coursesState === 'loaded' && draftCourses.length === 0 && <div className="panel docx-panel">
      <div className="docx-panel-heading"><div><h2>Сначала создайте черновой курс</h2><p>Импорт добавит в него новый урок. Опубликованные курсы не изменяются.</p></div></div>
      <form className="docx-form docx-course-form" onSubmit={handleCreateCourse}>
        <label className="docx-field"><span>Название курса</span><input className="docx-input" required maxLength={200} value={courseTitle} onChange={(event) => setCourseTitle(event.target.value)} placeholder="Например, Евангелие от Иоанна" /></label>
        <label className="docx-field"><span>Slug (латиницей)</span><input className="docx-input" required maxLength={100} value={courseSlug} onChange={(event) => setCourseSlug(event.target.value)} placeholder="john-gospel" autoCapitalize="none" autoCorrect="off" /></label>
        <button className="secondary-button" type="submit" disabled={createCourseBusy}>{createCourseBusy ? 'Создаём…' : 'Создать черновой курс'}</button>
      </form>
      {createCourseMessage && <p className={'docx-message ' + (createCourseMessage === 'Черновой курс создан.' ? 'docx-message--success' : 'docx-message--error')} role="status">{createCourseMessage}</p>}
    </div>}

    <div className="panel docx-panel">
      <div className="docx-panel-heading"><div><h2>Документ урока</h2><p>DOCX до 8 МБ. Сервер проверяет структуру документа перед сохранением.</p></div><span className="soft-pill">Только черновик</span></div>
      <div className="docx-form">
        <label className="docx-field"><span>Черновой курс</span>
          <select className="docx-input" value={selectedCourseID ?? ''} onChange={(event) => { setSelectedCourseID(event.target.value ? Number(event.target.value) : null); setImportedLesson(null); setImportError('') }} disabled={coursesState !== 'loaded' || draftCourses.length === 0}>
            <option value="">Выберите курс</option>
            {draftCourses.map((course) => <option key={course.id} value={course.id}>{course.title} · {course.lesson_count} уроков</option>)}
          </select>
        </label>
        {coursesState === 'loading' && <p className="catalog-message" role="status">Загружаем курсы…</p>}
        <label className="docx-field"><span>Файл DOCX</span><input ref={fileInput} className="docx-input docx-file-input" type="file" accept=".docx,application/vnd.openxmlformats-officedocument.wordprocessingml.document" disabled={previewState === 'loading' || importBusy} onChange={(event) => chooseFile(event.target.files?.[0] ?? null)} /></label>
        {file && <p className="docx-file-meta"><strong>{file.name}</strong><span>{(file.size / (1024 * 1024)).toFixed(2)} МБ</span></p>}
        <div className="docx-actions">
          <button className="secondary-button" type="button" onClick={() => void handlePreview()} disabled={!file || previewState === 'loading' || importBusy}>{previewState === 'loading' ? 'Читаем DOCX…' : 'Проверить содержимое'}</button>
          <button className="primary-button" type="button" onClick={() => void handleImport()} disabled={!file || !preview || previewedFile !== file || selectedCourseID === null || coursesState !== 'loaded' || importBusy || previewState === 'loading'}>{importBusy ? 'Сохраняем черновик…' : 'Импортировать как черновик'}</button>
        </div>
        <p className="docx-footnote">Предпросмотр показывает извлечённый текст, а не результат валидации. Финальная проверка вопросов и вариантов выполняется сервером при импорте. Публикация не выполняется.</p>
      </div>
      {previewError && <p className="docx-message docx-message--error" role="alert">{previewError}</p>}
      {importError && <p className="docx-message docx-message--error" role="alert">{importError}</p>}
      {preview && previewedFile === file && <div className="docx-preview-block">
        <div className="docx-preview-header"><h3>Предпросмотр извлечённого текста</h3><span>{preview.characters.toLocaleString('ru-RU')} символов</span></div>
        <pre className="docx-preview-text">{visiblePreview}{previewTruncated ? '\n\n… Предпросмотр сокращён. При импорте проверяется полный документ.' : ''}</pre>
        <p className="docx-message docx-message--info">Текст извлечён. Окончательная проверка структуры выполняется при импорте.</p>
      </div>}
    </div>

    {importedLesson && <div className="docx-message docx-message--success" role="status">
      <strong>Черновик сохранён: {importedLesson.title}</strong>
      <span>Версия {importedLesson.version_number} · {importedLesson.question_count} вопросов · ID версии {importedLesson.lesson_version_id}. Урок не опубликован.</span>
    </div>}
  </section>
}
