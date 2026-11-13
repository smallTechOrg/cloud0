import type { TargetRow as TargetData, TargetStatus } from '@/lib/attributionTypes';
import { useCurrency } from '@/lib/currency';
import { STATUS_LABEL, STATUS_TITLE, progressWidth, targetInputText, targetLabel } from '@/lib/dimensionFormat';
import { formatMoney, formatPct } from '@/lib/format';
import tableStyles from './CostTable.module.css';
import styles from './TargetsPanel.module.css';

export function StatusChip({ status }: { status: TargetStatus }) {
  return (
    <span className={`${styles.chip} ${styles[`chip_${status}`]}`} title={STATUS_TITLE[status]} data-testid="target-status" data-status={status}>
      {STATUS_LABEL[status]}
    </span>
  );
}

export function ProgressBar({ pct, status }: { pct: number | null; status: TargetStatus }) {
  return (
    <div
      className={styles.bar}
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={pct === null ? undefined : Math.round(progressWidth(pct))}
      aria-label={pct === null ? 'no target' : `${formatPct(pct).replace('+', '')} of target used`}
      data-testid="target-progress"
    >
      <div className={`${styles.fill} ${styles[`fill_${status}`]}`} style={{ width: `${progressWidth(pct)}%` }} />
    </div>
  );
}

interface Props {
  row: TargetData;
  /** Draft text of the target input; undefined = unedited. */
  draft: string | undefined;
  error?: string;
  disabled?: boolean;
  onDraft: (product: string, text: string) => void;
}

/** One editable account/product row of the Targets table. */
export function TargetRowItem({ row, draft, error, disabled = false, onDraft }: Props) {
  const currency = useCurrency();
  const isAccount = row.product === '*';
  const slug = isAccount ? 'account' : row.product;
  const value = draft ?? targetInputText(row.target);
  const errId = `target-err-${slug}`;
  return (
    <tr className={`${tableStyles.row} ${isAccount ? styles.account : ''}`} data-testid={`target-row-${slug}`} data-status={row.status}>
      <th scope="row" className={tableStyles.weekCell}>
        {targetLabel(row.product)}
        {row.share_pct !== undefined ? <div className={styles.hint}>{row.share_pct.toFixed(1)} % of the month</div> : null}
      </th>
      <td className={tableStyles.num}>
        <input
          className={`${styles.input} ${error ? styles.inputBad : ''}`}
          type="text"
          inputMode="decimal"
          value={value}
          placeholder="no target"
          disabled={disabled}
          aria-label={`Monthly target for ${targetLabel(row.product)}`}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? errId : undefined}
          data-testid={`target-input-${slug}`}
          onChange={(e) => onDraft(row.product, e.target.value)}
        />
        {error ? (
          <div className={styles.rowError} id={errId} role="alert" data-testid={`target-error-${slug}`}>
            {error}
          </div>
        ) : null}
        {row.target !== null && row.month_from ? <div className={styles.hint}>since {row.month_from}</div> : null}
      </td>
      <td className={tableStyles.num} data-testid={`target-net-${slug}`}>
        {formatMoney(row.net, currency)}
      </td>
      <td className={tableStyles.num} data-testid={`target-projected-${slug}`}>
        {formatMoney(row.projected, currency)}
      </td>
      <td className={tableStyles.num}>{row.remaining === null ? '—' : formatMoney(row.remaining, currency)}</td>
      <td>
        <StatusChip status={row.status} />
      </td>
      <td className={styles.progressCell}>
        <ProgressBar pct={row.pct} status={row.status} />
        <span className={styles.pctText}>{row.pct === null ? '—' : `${row.pct.toFixed(1)} %`}</span>
      </td>
    </tr>
  );
}

export default TargetRowItem;
