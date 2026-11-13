import type { ReactNode } from 'react';
import styles from './EmptyState.module.css';

interface Props {
  title: string;
  children: ReactNode;
  testId?: string;
}

/** Callout for "nothing here yet" with the one action that populates it. */
export function EmptyState({ title, children, testId = 'empty-state' }: Props) {
  return (
    <div className={styles.callout} data-testid={testId} role="status">
      <strong className={styles.title}>{title}</strong>
      <div className={styles.body}>{children}</div>
    </div>
  );
}
