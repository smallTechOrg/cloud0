import type { RightsizingCandidate } from '@/lib/attributionTypes';
import { sparkGeometry, sparkText } from '@/lib/rightsizingFormat';
import styles from './RightsizingPanel.module.css';

interface Props {
  candidate: RightsizingCandidate;
  /** CPU threshold line (percent) or null when the series has none. */
  threshold: number | null;
}

/** Tiny inline SVG bars, one per evidence week, with the threshold as a dashed line. */
export function EvidenceSpark({ candidate, threshold }: Props) {
  const points = candidate.evidence.points;
  const text = sparkText(candidate, threshold);
  if (points.length === 0) {
    return (
      <span className={styles.noSpark} data-testid="evidence-spark-none">
        {text}
      </span>
    );
  }
  const g = sparkGeometry(points, threshold);
  return (
    <span className={styles.sparkWrap} data-testid="evidence-spark">
      <svg className={styles.spark} width={g.width} height={g.height} viewBox={`0 0 ${g.width} ${g.height}`} role="img" aria-label={text}>
        <title>{text}</title>
        {g.bars.map((b) =>
          b.value === null ? (
            <rect key={b.week} x={b.x} y={g.height - 4} width={b.width} height={2} className={styles.sparkGap} />
          ) : (
            <rect key={b.week} x={b.x} y={b.y} width={b.width} height={Math.max(b.height, 1)} className={b.over ? styles.sparkOver : styles.sparkBar} />
          ),
        )}
        {g.thresholdY !== null ? <line x1={0} x2={g.width} y1={g.thresholdY} y2={g.thresholdY} className={styles.sparkLine} /> : null}
      </svg>
      <span className={styles.srOnly}>{text}</span>
    </span>
  );
}
