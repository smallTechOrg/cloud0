import { TABS, type TabId } from '@/lib/tabs';
import styles from './NavTabs.module.css';

interface Props {
  active: TabId;
}

/** Vertical tab list; each tab is a hash link so the browser back button works. */
export function NavTabs({ active }: Props) {
  return (
    <nav className={styles.nav} aria-label="Sections">
      {TABS.map((t) => {
        const isActive = t.id === active;
        const isStub = t.phase > 1;
        return (
          <a
            key={t.id}
            href={`#${t.id}`}
            className={`${styles.tab} ${isActive ? styles.active : ''} ${isStub ? styles.stub : ''}`}
            aria-current={isActive ? 'page' : undefined}
            data-testid={`nav-tab-${t.id}`}
            title={isStub ? `Arrives in Phase ${t.phase}` : undefined}
          >
            <span>{t.label}</span>
            {isStub ? (
              <span className={styles.phase} aria-label={`stub, arrives in Phase ${t.phase}`}>
                P{t.phase}
              </span>
            ) : null}
          </a>
        );
      })}
    </nav>
  );
}
