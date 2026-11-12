import { describe, expect, it } from 'vitest';
import { loginUrl, safeNext } from './login';

describe('safeNext', () => {
  it('keeps same-origin paths with their query', () => {
    expect(safeNext('/')).toBe('/');
    expect(safeNext('/alerts?week=2026-W45')).toBe('/alerts?week=2026-W45');
  });

  it('falls back to / for anything that could leave the site or loop', () => {
    for (const bad of [null, undefined, '', 'alerts', 'https://evil.example', '//evil.example', '/\\evil.example', '/login', '/login?next=%2F']) {
      expect(safeNext(bad), String(bad)).toBe('/');
    }
  });
});

describe('loginUrl', () => {
  it('returns to the current page and query after sign-in', () => {
    expect(loginUrl({ pathname: '/', search: '' })).toBe('/login?next=%2F');
    expect(loginUrl({ pathname: '/alerts', search: '?week=2026-W45' })).toBe('/login?next=%2Falerts%3Fweek%3D2026-W45');
  });

  it('round-trips through safeNext', () => {
    const url = new URL(loginUrl({ pathname: '/alerts', search: '?week=2026-W45' }), 'http://cloud0.test');
    expect(safeNext(url.searchParams.get('next'))).toBe('/alerts?week=2026-W45');
  });
});
