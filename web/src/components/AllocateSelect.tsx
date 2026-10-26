import { ALLOCATE_OPTIONS } from '@/lib/dimensionFormat';
import type { AllocateMode } from '@/lib/attributionTypes';
import styles from './CostByDimension.module.css';

interface Props {
  value: AllocateMode;
  onChange: (mode: AllocateMode) => void;
  disabled?: boolean;
  /** Distinguishes radio groups when two are mounted. */
  name?: string;
}

/** Shared-cost allocation mode: none / spread evenly / spread proportionally, each explained. */
export function AllocateSelect({ value, onChange, disabled = false, name = 'allocate' }: Props) {
  return (
    <fieldset className={styles.allocate} data-testid="allocate-select" disabled={disabled}>
      <legend className={styles.legend}>Shared cost</legend>
      {ALLOCATE_OPTIONS.map((o) => (
        <label key={o.mode} className={`${styles.option} ${value === o.mode ? styles.optionOn : ''}`} data-testid={`allocate-option-${o.mode}`}>
          <input type="radio" name={name} value={o.mode} checked={value === o.mode} onChange={() => onChange(o.mode)} />
          <span className={styles.optionText}>
            <strong>{o.label}</strong>
            <span className={styles.explain}>{o.explain}</span>
          </span>
        </label>
      ))}
    </fieldset>
  );
}

export default AllocateSelect;
