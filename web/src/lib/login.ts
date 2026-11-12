// Password access (spec/api.md → Password access): where /login sends you
// back to, and how a page gets to /login when the API says "unauthorized".

export const LOGIN_PATH = '/login';

/** The `next` target after sign-in: a same-origin path only, else "/". */
export function safeNext(next: string | null | undefined): string {
  if (!next || !next.startsWith('/') || next.startsWith('//') || next.startsWith('/\\')) return '/';
  if (next === LOGIN_PATH || next.startsWith(`${LOGIN_PATH}?`)) return '/';
  return next;
}

/** The sign-in URL that returns to the current page afterwards. */
export function loginUrl(location: { pathname: string; search: string }): string {
  return `${LOGIN_PATH}?next=${encodeURIComponent(location.pathname + location.search)}`;
}
