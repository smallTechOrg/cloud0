// Password access (spec/api.md → Password access): where /login sends you
// back to, and how a page gets to /login when the API says "unauthorized".

const LOGIN_PATH = '/login';
const ORIGIN = 'http://cloud0.invalid';

/**
 * The `next` target after sign-in: a path on this site only, else "/".
 * Parsed with the same URL parser the browser uses for navigation, so tricks
 * like "/\t/evil.example" or "/\\evil.example" (which the parser turns into
 * "//evil.example") cannot leave the site.
 */
export function safeNext(next: string | null | undefined): string {
  if (!next || !next.startsWith('/')) return '/';
  let url: URL;
  try {
    url = new URL(next, ORIGIN);
  } catch {
    return '/';
  }
  if (url.origin !== ORIGIN || url.pathname === LOGIN_PATH) return '/';
  return url.pathname + url.search + url.hash;
}

/** The sign-in URL that returns to the current page (tab included) afterwards. */
export function loginUrl(location: { pathname: string; search: string; hash: string }): string {
  return `${LOGIN_PATH}?next=${encodeURIComponent(location.pathname + location.search + location.hash)}`;
}

/**
 * Where to go after signing in. A server redirect cannot carry the #tab in
 * `next` (browsers never send it), but the browser keeps it on /login's own
 * URL, so it is used when `next` has none.
 */
export function afterLogin(next: string | null, loginHash: string): string {
  const target = safeNext(next);
  return target.includes('#') ? target : target + loginHash;
}
