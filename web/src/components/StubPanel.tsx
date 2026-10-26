import styles from './StubPanel.module.css';

interface Props {
  tab: string;
  phase: number;
  description: string;
}

/**
 * The stub pattern (spec/ui.md): a bordered panel with a "Not functional yet"
 * badge, "<Tab> — Coming in Phase N", one paragraph of what it will show and
 * the non-interactive notice. Never an empty table, never a blank panel.
 */
export function StubPanel({ tab, phase, description }: Props) {
  const headingId = `stub-${tab.toLowerCase().replace(/[^a-z0-9]+/g, '-')}`;
  return (
    <section className={styles.panel} data-testid="stub-panel" data-phase={phase} aria-labelledby={headingId}>
      <span className={styles.badge}>Not functional yet</span>
      <h2 id={headingId} className={styles.heading}>
        {tab} — Coming in Phase {phase}
      </h2>
      <p className={styles.description}>{description}</p>
      <p className={styles.note}>Nothing on this tab is interactive in the current build.</p>
    </section>
  );
}
