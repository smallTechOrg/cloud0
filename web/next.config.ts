import type { NextConfig } from 'next';

// The static export is embedded into the Go binary (internal/webui/dist) and
// served on the same origin as /api/v1 — that is the only tested path.
// `pnpm dev` is a two-server convenience: the dev server proxies /api/* to a
// running `./bin/cloud0 serve` on CLOUD0_PORT (default 8087). The rewrite is
// only defined in development because `output: 'export'` ignores custom routes.
const apiPort = process.env.CLOUD0_PORT ?? '8087';
const isDev = process.env.NODE_ENV === 'development';

const nextConfig: NextConfig = {
  output: 'export',
  trailingSlash: false,
  reactStrictMode: true,
  ...(isDev
    ? {
        async rewrites() {
          return [{ source: '/api/:path*', destination: `http://127.0.0.1:${apiPort}/api/:path*` }];
        },
      }
    : {}),
};

export default nextConfig;
