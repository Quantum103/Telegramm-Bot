export type Course = {
  id: number
  slug: string
  title: string
  description?: string
  status: string
  published_at?: string
  lesson_count: number
}

export type Lesson = {
  id: number
  course_id: number
  course_title: string
  position: number
  lesson_version_id: number
  version_number: number
  title: string
  description?: string
  published_at?: string
  question_count: number
}

export type StaffIdentity = {
  id: number
  display_name: string
  email: string
  roles: string[]
  must_change_password: boolean
}

export type StaffAccount = {
  id: number
  display_name: string
  email: string
  account_status: string
  roles: string[]
  must_change_password: boolean
  created_at: string
}

export type CreatedStaffAccount = {
  account: StaffAccount
  temporary_password: string
}

export type AdminStudent = {
  id: number
  telegram_id: number
  display_name: string
  telegram_username?: string
  preferred_name?: string
  account_status: string
  registered_at: string
  last_active_at?: string
  curator_staff_id?: number
  curator_display_name?: string
  total_enrollments: number
  in_progress_enrollments: number
  completed_enrollments: number
  last_learning_activity_at?: string
}

export type StaffSession = {
  staff: StaffIdentity
  expires_at: string
}

export type StaffLesson = {
  id: number
  course_id: number
  course_title: string
  position: number
  lesson_version_id: number
  version_number: number
  version_status: 'draft' | 'published' | 'archived'
  title: string
  description?: string
  published_at?: string
  question_count: number
}

export type DOCXPreview = {
  text: string
  characters: number
}

export type ImportedDraftLesson = {
  id: number
  course_id: number
  course_title: string
  position: number
  lesson_version_id: number
  version_number: number
  title: string
  description?: string
  question_count: number
}

type CoursesResponse = { courses: Course[] }
type LessonsResponse = { lessons: Lesson[] }
type StaffLoginResponse = { staff: StaffIdentity }
type StaffCoursesResponse = { courses: Course[] }
type StaffLessonsResponse = { lessons: StaffLesson[] }
type StaffAccountsResponse = { accounts: StaffAccount[] }
type AdminStudentsResponse = { students: AdminStudent[] }
type APIErrorBody = { error?: string; detail?: string }

async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(path, {
    method: 'GET',
    headers: { Accept: 'application/json' },
    cache: 'no-store',
    credentials: 'same-origin',
    signal,
  })
  return readJSONResponse<T>(response)
}

function asAPIErrorBody(value: unknown): APIErrorBody {
  if (typeof value !== 'object' || value === null) return {}
  const candidate = value as Record<string, unknown>
  return {
    ...(typeof candidate.error === 'string' ? { error: candidate.error } : {}),
    ...(typeof candidate.detail === 'string' ? { detail: candidate.detail } : {}),
  }
}

function errorMessage(status: number, body: APIErrorBody): string {
  if (body.error === 'invalid_lesson_document' && body.detail) {
    return `Структура DOCX не прошла проверку: ${body.detail}`
  }
  const messages: Record<string, string> = {
    invalid_credentials: 'Неверный адрес электронной почты или пароль.',
    rate_limited: 'Слишком много попыток входа. Повторите попытку позже.',
    unauthorized: 'Сессия завершена. Войдите в систему ещё раз.',
    insufficient_permissions: 'Для этого действия нужна роль администратора.',
    password_change_required: 'Перед продолжением установите собственный пароль.',
    self_status_change_forbidden: 'Нельзя отключить собственную учётную запись.',
    conflict: 'Операция конфликтует с текущими данными. Обновите страницу и повторите попытку.',
    invalid_current_password: 'Текущий пароль указан неверно.',
    csrf_token_invalid: 'Проверка безопасности не прошла. Перезагрузите страницу и войдите снова.',
    origin_not_allowed: 'Запрос отклонён проверкой источника. Перезагрузите страницу и повторите попытку.',
    invalid_request: 'Проверьте введённые данные.',
    invalid_input: 'Данные не прошли проверку. Проверьте поля и повторите попытку.',
    not_found: 'Курс не найден.',
    invalid_state: 'Действие недоступно в текущем состоянии. Для публикации урока нужны вопросы, а для курса — хотя бы один опубликованный урок.',
    invalid_docx: 'Не удалось прочитать DOCX. Проверьте файл и его формат.',
    invalid_multipart: 'Не удалось прочитать загрузку файла. Повторите попытку.',
    content_type_must_be_multipart: 'Для загрузки нужен файл DOCX.',
    docx_file_required: 'Выберите файл с расширением .docx.',
    exactly_one_file_required: 'За один раз можно загрузить только один файл.',
    file_too_large: 'Файл превышает допустимый размер 8 МБ.',
    request_too_large: 'Размер запроса превышает допустимый предел.',
    internal_error: 'На сервере произошла ошибка. Попробуйте позже.',
    temporarily_unavailable: 'Сервис временно недоступен. Попробуйте позже.',
  }
  if (body.error && messages[body.error]) return messages[body.error]
  if (status === 401) return 'Сессия завершена. Войдите в систему ещё раз.'
  if (status === 403) return 'Доступ запрещён. Для импорта нужна роль администратора.'
  if (status === 413) return 'Файл превышает допустимый размер 8 МБ.'
  if (status === 404) return 'Запрошенный ресурс не найден.'
  return 'Не удалось выполнить запрос. Попробуйте ещё раз.'
}

async function readJSONResponse<T>(response: Response): Promise<T> {
  const value: unknown = await response.json().catch(() => null)
  if (!response.ok) throw new Error(errorMessage(response.status, asAPIErrorBody(value)))
  return value as T
}

function csrfToken(): string {
  const cookie = document.cookie
    .split(';')
    .map((item) => item.trim())
    .find((item) => item.startsWith('staff_csrf='))
  if (!cookie || cookie.length === 'staff_csrf='.length) {
    throw new Error('Не найден токен защиты запроса. Обновите страницу и войдите снова.')
  }
  return decodeURIComponent(cookie.slice('staff_csrf='.length))
}

function csrfHeaders(): Record<string, string> {
  return { 'X-CSRF-Token': csrfToken() }
}

export async function listCourses(signal?: AbortSignal): Promise<Course[]> {
  const body = await getJSON<CoursesResponse>('/api/v1/courses', signal)
  if (!body || !Array.isArray(body.courses)) throw new Error('Сервер вернул некорректный список курсов.')
  return body.courses
}

export async function listCourseLessons(courseID: number, signal?: AbortSignal): Promise<Lesson[]> {
  if (!Number.isSafeInteger(courseID) || courseID <= 0) throw new Error('Некорректный идентификатор курса.')
  const body = await getJSON<LessonsResponse>(`/api/v1/courses/${courseID}/lessons`, signal)
  if (!body || !Array.isArray(body.lessons)) throw new Error('Сервер вернул некорректный список уроков.')
  return body.lessons
}

export async function getStaffSession(signal?: AbortSignal): Promise<StaffSession | null> {
  const response = await fetch('/api/v1/staff/auth/me', {
    method: 'GET', headers: { Accept: 'application/json' }, cache: 'no-store',
    credentials: 'same-origin', signal,
  })
  if (response.status === 401) return null
  return readJSONResponse<StaffSession>(response)
}

export async function loginStaff(email: string, password: string): Promise<StaffIdentity> {
  const response = await fetch('/api/v1/staff/auth/login', {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    cache: 'no-store', credentials: 'same-origin',
    body: JSON.stringify({ email: email.trim(), password }),
  })
  const body = await readJSONResponse<StaffLoginResponse>(response)
  if (!body?.staff || !Number.isSafeInteger(body.staff.id) || !body.staff.email) {
    throw new Error('Сервер вернул некорректный ответ входа.')
  }
  return body.staff
}

export async function logoutStaff(): Promise<void> {
  const response = await fetch('/api/v1/staff/auth/logout', {
    method: 'POST', headers: { Accept: 'application/json', ...csrfHeaders() },
    cache: 'no-store', credentials: 'same-origin',
  })
  if (response.status === 204) return
  const body: unknown = await response.json().catch(() => null)
  throw new Error(errorMessage(response.status, asAPIErrorBody(body)))
}

export async function listStaffCourses(signal?: AbortSignal): Promise<Course[]> {
  const response = await fetch('/api/v1/staff/admin/courses', {
    method: 'GET', headers: { Accept: 'application/json' },
    cache: 'no-store', credentials: 'same-origin', signal,
  })
  const body = await readJSONResponse<StaffCoursesResponse>(response)
  if (!body || !Array.isArray(body.courses)) {
    throw new Error('Сервер вернул некорректный список курсов для администратора.')
  }
  return body.courses
}

export async function createStaffDraftCourse(title: string, slug: string): Promise<Course> {
  const response = await fetch('/api/v1/staff/admin/courses', {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...csrfHeaders() },
    cache: 'no-store', credentials: 'same-origin',
    body: JSON.stringify({ title: title.trim(), slug: slug.trim(), description: null }),
  })
  return readJSONResponse<Course>(response)
}

export async function listStaffLessonsForCourse(courseID: number, signal?: AbortSignal): Promise<StaffLesson[]> {
  if (!Number.isSafeInteger(courseID) || courseID <= 0) throw new Error('Некорректный идентификатор курса.')
  const body = await readJSONResponse<StaffLessonsResponse>(await fetch(
    `/api/v1/staff/admin/courses/${courseID}/lessons`,
    { method: 'GET', headers: { Accept: 'application/json' }, cache: 'no-store', credentials: 'same-origin', signal },
  ))
  if (!body || !Array.isArray(body.lessons)) throw new Error('Сервер вернул некорректный список уроков курса.')
  return body.lessons
}

export async function publishStaffLessonVersion(versionID: number): Promise<void> {
  if (!Number.isSafeInteger(versionID) || versionID <= 0) throw new Error('Некорректный идентификатор версии урока.')
  const response = await fetch(`/api/v1/staff/admin/lesson-versions/${versionID}/publish`, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...csrfHeaders() },
    cache: 'no-store', credentials: 'same-origin', body: '{}',
  })
  await readJSONResponse<unknown>(response)
}

export async function publishStaffCourse(courseID: number): Promise<void> {
  if (!Number.isSafeInteger(courseID) || courseID <= 0) throw new Error('Некорректный идентификатор курса.')
  const response = await fetch(`/api/v1/staff/admin/courses/${courseID}/publish`, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...csrfHeaders() },
    cache: 'no-store', credentials: 'same-origin', body: '{}',
  })
  await readJSONResponse<unknown>(response)
}

export async function previewDOCX(file: File): Promise<DOCXPreview> {
  const form = new FormData()
  form.append('document', file)
  const response = await fetch('/api/v1/staff/admin/import/docx/preview', {
    method: 'POST', headers: { Accept: 'application/json', ...csrfHeaders() },
    cache: 'no-store', credentials: 'same-origin', body: form,
  })
  const body = await readJSONResponse<DOCXPreview>(response)
  if (typeof body?.text !== 'string' || typeof body.characters !== 'number') {
    throw new Error('Сервер вернул некорректный предпросмотр документа.')
  }
  return body
}

export async function importDOCXToDraft(courseID: number, file: File): Promise<ImportedDraftLesson> {
  if (!Number.isSafeInteger(courseID) || courseID <= 0) throw new Error('Выберите черновой курс.')
  const form = new FormData()
  form.append('document', file)
  const response = await fetch(`/api/v1/staff/admin/courses/${courseID}/import/docx`, {
    method: 'POST', headers: { Accept: 'application/json', ...csrfHeaders() },
    cache: 'no-store', credentials: 'same-origin', body: form,
  })
  return readJSONResponse<ImportedDraftLesson>(response)
}

export async function changeStaffPassword(currentPassword: string, newPassword: string): Promise<void> {
  const response = await fetch('/api/v1/staff/auth/password', {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...csrfHeaders() },
    cache: 'no-store', credentials: 'same-origin',
    body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
  })
  if (response.status === 204) return
  const body: unknown = await response.json().catch(() => null)
  throw new Error(errorMessage(response.status, asAPIErrorBody(body)))
}

export async function listStaffAccounts(signal?: AbortSignal): Promise<StaffAccount[]> {
  const response = await fetch('/api/v1/staff/admin/staff', {
    method: 'GET', headers: { Accept: 'application/json' },
    cache: 'no-store', credentials: 'same-origin', signal,
  })
  const body = await readJSONResponse<StaffAccountsResponse>(response)
  if (!body || !Array.isArray(body.accounts)) throw new Error('Сервер вернул некорректный список сотрудников.')
  return body.accounts
}

export async function createStaffAccount(displayName: string, email: string, roleCode: 'administrator' | 'curator'): Promise<CreatedStaffAccount> {
  const response = await fetch('/api/v1/staff/admin/staff', {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...csrfHeaders() },
    cache: 'no-store', credentials: 'same-origin',
    body: JSON.stringify({ display_name: displayName.trim(), email: email.trim(), role_code: roleCode }),
  })
  const body = await readJSONResponse<CreatedStaffAccount>(response)
  if (!body?.account || typeof body.temporary_password !== 'string' || body.temporary_password.length < 32) {
    throw new Error('Сервер вернул некорректный результат создания сотрудника.')
  }
  return body
}

export async function listAdminStudents(signal?: AbortSignal): Promise<AdminStudent[]> {
  const response = await fetch('/api/v1/staff/admin/students', {
    method: 'GET', headers: { Accept: 'application/json' },
    cache: 'no-store', credentials: 'same-origin', signal,
  })
  const body = await readJSONResponse<AdminStudentsResponse>(response)
  if (!body || !Array.isArray(body.students)) throw new Error('Сервер вернул некорректный список учеников.')
  return body.students
}

export async function assignStudentCurator(studentID: number, curatorStaffID: number): Promise<void> {
  if (!Number.isSafeInteger(studentID) || studentID <= 0 || !Number.isSafeInteger(curatorStaffID) || curatorStaffID <= 0) {
    throw new Error('Некорректный идентификатор ученика или куратора.')
  }
  const response = await fetch(`/api/v1/staff/admin/students/${studentID}/curator`, {
    method: 'PUT',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...csrfHeaders() },
    cache: 'no-store', credentials: 'same-origin',
    body: JSON.stringify({ curator_staff_id: curatorStaffID }),
  })
  await readJSONResponse<unknown>(response)
}

export async function endStudentCuratorAssignment(studentID: number): Promise<void> {
  if (!Number.isSafeInteger(studentID) || studentID <= 0) throw new Error('Некорректный идентификатор ученика.')
  const response = await fetch(`/api/v1/staff/admin/students/${studentID}/curator`, {
    method: 'DELETE', headers: { Accept: 'application/json', ...csrfHeaders() },
    cache: 'no-store', credentials: 'same-origin',
  })
  await readJSONResponse<unknown>(response)
}

export async function setStaffAccountStatus(staffID: number, status: 'active' | 'inactive'): Promise<void> {
  if (!Number.isSafeInteger(staffID) || staffID <= 0) throw new Error('Некорректный идентификатор сотрудника.')
  const response = await fetch(`/api/v1/staff/admin/staff/${staffID}/status`, {
    method: 'PATCH',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...csrfHeaders() },
    cache: 'no-store', credentials: 'same-origin',
    body: JSON.stringify({ account_status: status }),
  })
  await readJSONResponse<unknown>(response)
}

export async function resetStaffPassword(staffID: number): Promise<string> {
  if (!Number.isSafeInteger(staffID) || staffID <= 0) throw new Error('Некорректный идентификатор сотрудника.')
  const response = await fetch(`/api/v1/staff/admin/staff/${staffID}/reset-password`, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...csrfHeaders() },
    cache: 'no-store', credentials: 'same-origin', body: '{}',
  })
  const body = await readJSONResponse<{ temporary_password: string }>(response)
  if (typeof body?.temporary_password !== 'string' || body.temporary_password.length < 32) {
    throw new Error('Сервер вернул некорректный временный пароль.')
  }
  return body.temporary_password
}


export type StaffConversationSummary = {
  id: number
  student_id: number
  student_display_name: string
  status: string
  unread_count: number
  last_activity_at: string
  last_message?: StaffConversationMessage
}

export type StaffConversationMessage = {
  id: number
  conversation_id: number
  sender_type: 'student' | 'curator'
  message_type: string
  body_text?: string
  created_at: string
  delivery_status?: string
  delivery_error_code?: string
}

export type StaffTelegramBindingStatus = {
  linked: boolean
  telegram_username?: string
}

export type StaffTelegramLinkToken = {
  token: string
  expires_at: string
}

export type CuratorNotification = {
  id: number
  message_id: number
  conversation_id: number
  student_display_name: string
  channel: string
  delivery_status: string
  attempt_count: number
  last_error_code?: string
  last_error_at?: string
  sent_at?: string
  created_at: string
}

export type StaffNotificationPreferences = {
  reminders_enabled: boolean
  notifications_enabled: boolean
  timezone: string
  quiet_hours_enabled: boolean
  quiet_start: string
  quiet_end: string
}

type StaffConversationsResponse = { conversations: StaffConversationSummary[] }
type StaffConversationMessagesResponse = { messages: StaffConversationMessage[] }
type StaffTelegramNotificationsResponse = { notifications: CuratorNotification[] }

export function staffConversationEventsURL(): string {
  const url = new URL('/api/v1/staff/conversations/events', window.location.href)
  url.protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return url.toString()
}

export async function listStaffConversations(signal?: AbortSignal): Promise<StaffConversationSummary[]> {
  const body = await getJSON<StaffConversationsResponse>('/api/v1/staff/conversations', signal)
  if (!body || !Array.isArray(body.conversations)) throw new Error('Сервер вернул некорректный список диалогов.')
  return body.conversations
}

export async function listStaffConversationMessages(conversationID: number, signal?: AbortSignal): Promise<StaffConversationMessage[]> {
  if (!Number.isSafeInteger(conversationID) || conversationID <= 0) throw new Error('Некорректный идентификатор диалога.')
  const body = await getJSON<StaffConversationMessagesResponse>(`/api/v1/staff/conversations/${conversationID}/messages?limit=100`, signal)
  if (!body || !Array.isArray(body.messages)) throw new Error('Сервер вернул некорректную историю диалога.')
  return body.messages
}

export async function sendStaffConversationMessage(conversationID: number, bodyText: string): Promise<StaffConversationMessage> {
  if (!Number.isSafeInteger(conversationID) || conversationID <= 0 || !bodyText.trim()) throw new Error('Введите текст ответа.')
  const response = await fetch(`/api/v1/staff/conversations/${conversationID}/messages`, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...csrfHeaders() },
    cache: 'no-store', credentials: 'same-origin', body: JSON.stringify({ body_text: bodyText.trim() }),
  })
  return readJSONResponse<StaffConversationMessage>(response)
}

export async function markStaffConversationRead(conversationID: number): Promise<void> {
  if (!Number.isSafeInteger(conversationID) || conversationID <= 0) throw new Error('Некорректный идентификатор диалога.')
  const response = await fetch(`/api/v1/staff/conversations/${conversationID}/read`, {
    method: 'POST', headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...csrfHeaders() },
    cache: 'no-store', credentials: 'same-origin', body: '{}',
  })
  await readJSONResponse<unknown>(response)
}

export async function getStaffTelegramBindingStatus(signal?: AbortSignal): Promise<StaffTelegramBindingStatus> {
  return getJSON<StaffTelegramBindingStatus>('/api/v1/staff/telegram/status', signal)
}

export async function createStaffTelegramLinkToken(): Promise<StaffTelegramLinkToken> {
  const response = await fetch('/api/v1/staff/telegram/link-token', {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...csrfHeaders() },
    cache: 'no-store', credentials: 'same-origin', body: '{}',
  })
  return readJSONResponse<StaffTelegramLinkToken>(response)
}


export async function getStaffNotificationPreferences(signal?: AbortSignal): Promise<StaffNotificationPreferences> {
  return getJSON<StaffNotificationPreferences>('/api/v1/staff/telegram/preferences', signal)
}

export async function updateStaffNotificationPreferences(preferences: StaffNotificationPreferences): Promise<void> {
  const response = await fetch('/api/v1/staff/telegram/preferences', {
    method: 'PUT',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...csrfHeaders() },
    cache: 'no-store', credentials: 'same-origin', body: JSON.stringify(preferences),
  })
  await readJSONResponse<StaffNotificationPreferences>(response)
}

export async function listCuratorTelegramNotifications(signal?: AbortSignal): Promise<CuratorNotification[]> {
  const body = await getJSON<StaffTelegramNotificationsResponse>('/api/v1/staff/telegram/notifications', signal)
  if (!body || !Array.isArray(body.notifications)) throw new Error('Сервер вернул некорректный журнал уведомлений.')
  return body.notifications
}
