import { useEffect, useMemo, useState } from 'react'
import './mini-app.css'

type BibleVerse = {
  number: number
  text: string
}

type BibleChapter = {
  book: string
  chapter: number
  translation?: string
  verses: BibleVerse[]
}

type TelegramWebApp = {
  ready: () => void
  expand: () => void
  close: () => void
  setHeaderColor?: (color: string) => void
  setBackgroundColor?: (color: string) => void
}

declare global {
  interface Window {
    Telegram?: { WebApp?: TelegramWebApp }
  }
}

function positiveInteger(value: string | null): number | null {
  if (!value || !/^\d+$/.test(value)) return null
  const parsed = Number(value)
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : null
}

function parseHighlightedVerses(value: string | null): Set<number> {
  const result = new Set<number>()
  if (!value) return result

  for (const part of value.split(',')) {
    const match = /^(\d+)(?:-(\d+))?$/.exec(part.trim())
    if (!match) continue
    const start = Number(match[1])
    const end = Number(match[2] ?? match[1])
    if (start < 1 || end < start || end - start > 200) continue
    for (let verse = start; verse <= end; verse += 1) result.add(verse)
  }
  return result
}

function parseBibleChapter(value: unknown): BibleChapter | null {
  if (typeof value !== 'object' || value === null) return null
  const candidate = value as Record<string, unknown>
  if (
    typeof candidate.book !== 'string' ||
    typeof candidate.chapter !== 'number' ||
    !Number.isSafeInteger(candidate.chapter) ||
    !Array.isArray(candidate.verses)
  ) return null

  const verses: BibleVerse[] = []
  const seen = new Set<number>()
  for (const entry of candidate.verses) {
    if (typeof entry !== 'object' || entry === null) return null
    const verse = entry as Record<string, unknown>
    if (
      typeof verse.number !== 'number' ||
      !Number.isSafeInteger(verse.number) ||
      verse.number < 1 ||
      typeof verse.text !== 'string' ||
      !verse.text.trim() ||
      seen.has(verse.number)
    ) return null
    seen.add(verse.number)
    verses.push({ number: verse.number, text: verse.text })
  }
  if (verses.length === 0 || verses.length > 200) return null
  verses.sort((left, right) => left.number - right.number)

  return {
    book: candidate.book,
    chapter: candidate.chapter,
    translation: typeof candidate.translation === 'string' ? candidate.translation : undefined,
    verses,
  }
}

export default function TelegramMiniApp() {
  const params = useMemo(() => new URLSearchParams(window.location.search), [])
  const bookID = positiveInteger(params.get('book_id'))
  const chapterNumber = positiveInteger(params.get('chapter'))
  const bookName = params.get('book')?.trim() || 'Библия'
  const question = params.get('question')?.trim() ?? ''
  const reference = params.get('reference')?.trim() ?? ''
  const highlightedVerses = useMemo(
    () => parseHighlightedVerses(params.get('highlight')),
    [params],
  )
  const [chapter, setChapter] = useState<BibleChapter | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [fontSize, setFontSize] = useState(19)

  useEffect(() => {
    const webApp = window.Telegram?.WebApp
    webApp?.ready()
    webApp?.expand()
    webApp?.setHeaderColor?.('#28205e')
    webApp?.setBackgroundColor?.('#f5f3ff')

    if (!bookID || !chapterNumber) {
      setError('В ссылке не указана книга или глава.')
      setLoading(false)
      return
    }

    const controller = new AbortController()
    setLoading(true)
    setError('')
    void fetch(`/bible/synodal/${bookID}/${chapterNumber}.json`, {
      headers: { Accept: 'application/json' },
      cache: 'no-store',
      signal: controller.signal,
    }).then(async (response) => {
      if (!response.ok) {
        throw new Error(response.status === 404
          ? 'Эта глава ещё не добавлена в библиотеку проекта.'
          : 'Не удалось загрузить главу.')
      }
      const chapterData = parseBibleChapter(await response.json())
      if (!chapterData) throw new Error('Файл главы имеет неверный формат.')
      if (chapterData.chapter !== chapterNumber) throw new Error('В файле указан другой номер главы.')
      setChapter(chapterData)
    }).catch((loadError: unknown) => {
      if (controller.signal.aborted) return
      setError(loadError instanceof Error ? loadError.message : 'Не удалось загрузить главу.')
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false)
    })
    return () => controller.abort()
  }, [bookID, chapterNumber])

  function closeReader() {
    const webApp = window.Telegram?.WebApp
    if (webApp) {
      webApp.close()
      return
    }
    window.history.back()
  }

  return (
    <main className="mini-app-shell">
      <div className="mini-app-container">
        <header className="mini-app-topbar">
          <div className="mini-app-brand">
            <span className="mini-app-brand-mark">✦</span>
            <span>СЛОВО</span>
            <span className="mini-app-brand-divider">/</span>
            <span className="mini-app-brand-muted">ЧТЕНИЕ БИБЛИИ</span>
          </div>
          <button className="mini-app-close" type="button" onClick={closeReader} aria-label="Закрыть чтение">
            ✕
          </button>
        </header>

        <section className="mini-app-hero">
          <div className="mini-app-hero-orbit mini-app-hero-orbit-one" />
          <div className="mini-app-hero-orbit mini-app-hero-orbit-two" />
          <div className="mini-app-kicker"><span /> ВОПРОС УРОКА</div>
          <h1>{question || `${bookName} · глава ${chapterNumber ?? ''}`}</h1>
          <div className="mini-app-hero-footer">
            <span className="mini-app-reference-pill">📖 {reference || `${bookName} ${chapterNumber ?? ''}`}</span>
            {highlightedVerses.size > 0 && <span className="mini-app-highlight-pill">✦ {highlightedVerses.size} выделенных стихов</span>}
          </div>
        </section>

        <section className="mini-app-chapter-heading">
          <div>
            <p className="mini-app-section-label">ПОЛНЫЙ ТЕКСТ</p>
            <h2>{chapter?.book ?? bookName} <span>{chapterNumber ?? ''}</span></h2>
            <p className="mini-app-translation">{chapter?.translation || 'Синодальный перевод'}</p>
          </div>
          <div className="mini-app-font-controls" aria-label="Размер текста">
            <button type="button" onClick={() => setFontSize((value) => Math.max(16, value - 1))} disabled={fontSize <= 16} aria-label="Уменьшить текст">A−</button>
            <button type="button" onClick={() => setFontSize((value) => Math.min(25, value + 1))} disabled={fontSize >= 25} aria-label="Увеличить текст">A+</button>
          </div>
        </section>

        {loading && <section className="mini-app-state-card"><span className="mini-app-spinner" /> Загружаем главу…</section>}

        {!loading && error && (
          <section className="mini-app-state-card mini-app-error-card">
            <span className="mini-app-state-icon">📚</span>
            <h3>Глава пока недоступна</h3>
            <p>{error}</p>
            {bookID && chapterNumber && (
              <code>web/public/bible/synodal/{bookID}/{chapterNumber}.json</code>
            )}
            <button type="button" onClick={closeReader}>Вернуться к вопросу</button>
          </section>
        )}

        {!loading && chapter && (
          <>
            <div className="mini-app-reading-note">
              <span>🟡</span>
              <p>Цветом отмечены стихи, на которые опирается вопрос. Остальная глава сохранена без изменений.</p>
            </div>
            <section className="mini-app-verses" aria-label={`${chapter.book}, глава ${chapter.chapter}`}>
              {chapter.verses.map((verse) => {
                const highlighted = highlightedVerses.has(verse.number)
                return (
                  <article
                    className={`mini-app-verse${highlighted ? ' mini-app-verse-highlighted' : ''}`}
                    key={verse.number}
                  >
                    <span className="mini-app-verse-number">{verse.number}</span>
                    <p style={{ fontSize }}>{verse.text}</p>
                    {highlighted && <span className="mini-app-verse-mark" aria-label="Стих используется в вопросе">✦</span>}
                  </article>
                )
              })}
            </section>
            <footer className="mini-app-footer">
              <span>КОНЕЦ ГЛАВЫ · {chapter.verses.length} СТИХОВ</span>
              <button type="button" onClick={closeReader}>← Вернуться к вопросу</button>
            </footer>
          </>
        )}
      </div>
    </main>
  )
}
