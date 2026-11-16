import { describe, expect, it } from 'vitest';
import { afterLogin, loginUrl, safeNext } from './login';

describe('safeNext', () => {
  it('keeps same-site paths with their query and tab', () => {
    expect(safeNext('/')).toBe('/');
    expect(safeNext('/alerts?week=2026-W45')).toBe('/alerts?week=2026-W45');
    expect(safeNext('/#alerts')).toBe('/#alerts');
  });

  it('falls back to / for anything that could leave the site or loop', () => {
    for (const bad of [null, undefined, '', 'alerts', 'https://evil.example', '//evil.example', '/\\evil.example', '/login', '/login?next=%2F']) {
      expect(safeNext(bad), String(bad)).toBe('/');
    }
  });

  it('is not fooled by tabs or newlines the URL parser strips', () => {
    for (const bad of ['/\t/evil.example', '/\n/evil.example', '/\r/evil.example', '/\t\\evil.example', '/\t/evil.example/path?q=1']) {
      expect(safeNext(bad), JSON.stringify(bad)).toBe('/');
    }
  });
});

describe('loginUrl', () => {
  it('returns to the current page, query and tab after sign-in', () => {
    expect(loginUrl({ pathname: '/', search: '', hash: '' })).toBe('/login?next=%2F');
    expect(loginUrl({ pathname: '/', search: '?week=2026-W45', hash: '#alerts' })).toBe('/login?next=%2F%3Fweek%3D2026-W45%23alerts');
  });

  it('round-trips through safeNext', () => {
    const url = new URL(loginUrl({ pathname: '/', search: '?week=2026-W45', hash: '#alerts' }), 'http://cloud0.test');
    expect(safeNext(url.searchParams.get('next'))).toBe('/?week=2026-W45#alerts');
  });
});

describe('afterLogin', () => {
  it('keeps the tab the browser carried over from a server redirect', () => {
    expect(afterLogin('/', '#alerts')).toBe('/#alerts');
  });

  it('prefers the tab already in next, and never leaves the site', () => {
    expect(afterLogin('/#budgets', '#alerts')).toBe('/#budgets');
    expect(afterLogin('//evil.example', '#alerts')).toBe('/#alerts');
  });
});
