export type Course = {
  id: number
  slug: string
  title: string
  description?: string
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

type CoursesResponse = { courses: Course[] }
type LessonsResponse = { lessons: Lesson[] }

async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(path, {
    method: 'GET',
    headers: { Accept: 'application/json' },
    cache: 'no-store',
    credentials: 'same-origin',
    signal,
  })

  if (!response.ok) {
    throw new Error(response.status === 404 ? 'Запрошенные данные не найдены.' : 'Не удалось загрузить данные с сервера.')
  }

  return response.json() as Promise<T>
}

export async function listCourses(signal?: AbortSignal): Promise<Course[]> {
  const body = await getJSON<CoursesResponse>('/api/v1/courses', signal)
  if (!body || !Array.isArray(body.courses)) {
    throw new Error('Сервер вернул некорректный список курсов.')
  }
  return body.courses
}

export async function listCourseLessons(courseID: number, signal?: AbortSignal): Promise<Lesson[]> {
  if (!Number.isSafeInteger(courseID) || courseID <= 0) {
    throw new Error('Некорректный идентификатор курса.')
  }

  const body = await getJSON<LessonsResponse>(`/api/v1/courses/${courseID}/lessons`, signal)
  if (!body || !Array.isArray(body.lessons)) {
    throw new Error('Сервер вернул некорректный список уроков.')
  }
  return body.lessons
}
