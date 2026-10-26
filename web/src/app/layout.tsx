import type { Metadata } from 'next';
import type { ReactNode } from 'react';
import './globals.css';

export const metadata: Metadata = {
  title: 'cloud0 — weekly GCP cost review',
  description: 'Internal dashboard: GCP billing per ISO week, net of credits.',
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <body data-cloud0-ui="real">{children}</body>
    </html>
  );
}
