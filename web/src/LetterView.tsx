import { useState } from 'react'
import { errorText } from './common'
import { IconDoc } from './Icons'
import { QR, receiptURL } from './Receipt'
import { useLetterQuery } from './store'

const langs = [
  { code: 'en', label: 'English' },
  { code: 'es', label: 'Español' },
] as const

/**
 * The decision's letter, as it would be sent: written from the decision by template, never by a model, so it says
 * nothing the decision does not. Print prints the letter alone.
 */
export function LetterView({ id, portal = false }: { id: string; portal?: boolean }) {
  const [lang, setLang] = useState<'en' | 'es'>('en')
  const [open, setOpen] = useState(false)
  const { data, error, isFetching } = useLetterQuery({ id, lang, portal }, { skip: !open })
  return (
    <details className="mt-4" onToggle={(e) => setOpen((e.target as HTMLDetailsElement).open)}>
      <summary className="flex cursor-pointer items-center gap-2 font-semibold">
        <IconDoc size={16} /> The letter, in plain words
      </summary>
      <div className="mt-3 flex flex-wrap items-center gap-2 print:hidden">
        <div role="radiogroup" aria-label="Language" className="flex gap-2">
          {langs.map((l) => (
            <button
              key={l.code}
              type="button"
              role="radio"
              aria-checked={lang === l.code}
              className={`chip ${lang === l.code ? 'on' : ''}`}
              onClick={() => setLang(l.code)}
            >
              {l.label}
            </button>
          ))}
        </div>
        <button type="button" className="link" onClick={() => window.print()} disabled={!data}>
          Print
        </button>
        <span className="meta text-xs">Written from the decision by fixed templates; no model writes any of it.</span>
      </div>
      {isFetching && <p className="meta">Writing…</p>}
      {error && <p role="alert">{errorText(error, 'The letter could not be written.')}</p>}
      {data && !isFetching && (
        <article className="letter" lang={data.lang} aria-label="Letter">
          <header className="flex items-start justify-between gap-6">
            <div>
              <h3 className="letter-title">{data.title}</h3>
              <p className="letter-date">{data.date}</p>
            </div>
            {data.receipt && (
              <figure className="m-0 text-center">
                <QR text={receiptURL(data.receipt)} size={96} label={`QR code for receipt ${data.receipt}`} />
                <figcaption className="letter-receipt">{data.receipt}</figcaption>
              </figure>
            )}
          </header>
          {data.sections.map((s, i) => (
            <section key={i}>
              {s.heading && <h4>{s.heading}</h4>}
              {s.paragraphs?.map((p, j) => <p key={j}>{p}</p>)}
              {s.quote && <blockquote lang="en">“{s.quote}”</blockquote>}
              {s.items && s.items.length > 0 && (
                <ul>
                  {s.items.map((it, j) => (
                    <li key={j}>{it}</li>
                  ))}
                </ul>
              )}
              {s.after?.map((p, j) => <p key={`a${j}`}>{p}</p>)}
            </section>
          ))}
          {data.quoted && <p className="letter-note">{data.quoted}</p>}
        </article>
      )}
    </details>
  )
}
