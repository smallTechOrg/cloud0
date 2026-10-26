import type { WeekID } from '@/lib/types';
import styles from './WeekPicker.module.css';

export interface WeekOption {
  id: WeekID;
  /** `2026-W43 · Oct 19–25 · settling` */
  label: string;
}

interface Props {
  /** Newest first. */
  options: WeekOption[];
  value: WeekID | null;
  onChange: (week: WeekID) => void;
  disabled?: boolean;
}

export function WeekPicker({ options, value, onChange, disabled = false }: Props) {
  const empty = options.length === 0;
  return (
    <label className={styles.field}>
      <span className={styles.label}>Week</span>
      <select
        className={styles.select}
        data-testid="week-picker"
        value={value ?? ''}
        disabled={disabled || empty}
        onChange={(e) => onChange(e.target.value)}
        aria-busy={empty || undefined}
      >
        {empty ? <option value="">loading weeks…</option> : null}
        {options.map((o) => (
          <option key={o.id} value={o.id}>
            {o.label}
          </option>
        ))}
      </select>
    </label>
  );
}
