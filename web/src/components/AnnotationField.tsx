import { useEffect, useState } from 'react';
import { ApiError, toApiError } from '@/lib/api';
import { formatTimeZ } from '@/lib/format';
import { ANNOTATION_MAX_CHARS, type Annotation, type WeekID } from '@/lib/types';
import styles from './AnnotationField.module.css';

interface Props {
  week: WeekID;
  /** The stored note (from the table row or GET /annotations/{week}); null = no note. */
  initialBody: string | null;
  loading: boolean;
  loadError: ApiError | null;
  onSave: (body: string) => Promise<Annotation>;
}

/** "Note for 2026-W43" · textarea with counter · Save · "Saved 09:12Z". */
export function AnnotationField({ week, initialBody, loading, loadError, onSave }: Props) {
  const [text, setText] = useState(initialBody ?? '');
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [savedAt, setSavedAt] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Follow the stored note until the user starts typing.
  useEffect(() => {
    if (!dirty) setText(initialBody ?? '');
  }, [initialBody, dirty]);

  async function save() {
    setSaving(true);
    setError(null);
    try {
      const saved = await onSave(text);
      setDirty(false);
      setText(saved.body);
      setSavedAt(saved.updated_at);
    } catch (err) {
      const e = toApiError(err);
      // Keep the typed text; show the error with its hint.
      setError(`${e.code} — ${e.message}${e.hint ? ` ${e.hint}` : ''}`);
    } finally {
      setSaving(false);
    }
  }

  const tooLong = text.length > ANNOTATION_MAX_CHARS;
  const hasNote = text.trim().length > 0;
  let indicator: string;
  if (saving) indicator = 'Saving…';
  else if (savedAt) indicator = `Saved ${formatTimeZ(savedAt)}`;
  else if (savedAt === null && !dirty && !hasNote && initialBody === null) indicator = 'No note';
  else if (dirty) indicator = 'Unsaved changes';
  else indicator = 'Stored note';

  return (
    <section className={styles.section} aria-labelledby="annotation-heading">
      <div className={styles.head}>
        <h3 id="annotation-heading" className={styles.heading}>
          Note for {week}
        </h3>
        <span className={styles.indicator} data-testid="annotation-saved" role="status">
          {indicator}
        </span>
      </div>
      {loadError ? (
        <p className={styles.error} role="alert">
          Could not load the note: <code>{loadError.code}</code> — {loadError.message}
          {loadError.hint ? <strong> {loadError.hint}</strong> : null}
        </p>
      ) : null}
      <textarea
        className={styles.textarea}
        data-testid="annotation-input"
        value={text}
        placeholder={loading ? 'Loading note…' : 'What happened this week? (free text, saved with the week)'}
        maxLength={ANNOTATION_MAX_CHARS + 1}
        rows={3}
        onChange={(e) => {
          setText(e.target.value);
          setDirty(true);
        }}
        aria-label={`Note for ${week}`}
      />
      <div className={styles.foot}>
        <span className={`${styles.counter} ${tooLong ? styles.counterOver : ''}`}>
          {text.length.toLocaleString('en-US')} / {ANNOTATION_MAX_CHARS.toLocaleString('en-US')}
        </span>
        {error ? (
          <span className={styles.error} role="alert" data-testid="annotation-error">
            {error}
          </span>
        ) : null}
        <button
          type="button"
          className={styles.save}
          data-testid="annotation-save"
          onClick={save}
          disabled={saving || tooLong || (!dirty && !hasNote)}
          title={hasNote ? 'Save the note for this week' : 'Saving an empty note deletes it'}
        >
          {saving ? 'Saving…' : hasNote ? 'Save' : 'Delete note'}
        </button>
      </div>
    </section>
  );
}
