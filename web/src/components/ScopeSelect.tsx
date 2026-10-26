import { SCOPE_ALL, type Scope } from '@/lib/types';
import styles from './ScopeSelect.module.css';

interface Props {
  projects: string[];
  value: Scope;
  onChange: (scope: Scope) => void;
  disabled?: boolean;
}

export const SCOPE_ALL_LABEL = 'All billed projects';

/** Human label for a scope value. */
export function scopeLabel(scope: Scope): string {
  return scope === SCOPE_ALL ? SCOPE_ALL_LABEL : scope;
}

export function ScopeSelect({ projects, value, onChange, disabled = false }: Props) {
  return (
    <label className={styles.field}>
      <span className={styles.label}>Scope</span>
      <select
        className={styles.select}
        data-testid="scope-select"
        value={value}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value)}
      >
        <option value={SCOPE_ALL}>{SCOPE_ALL_LABEL}</option>
        {projects.map((p) => (
          <option key={p} value={p}>
            {p}
          </option>
        ))}
      </select>
    </label>
  );
}
