import type { ReactNode } from 'react';
import { Header } from './Header';

interface LayoutProps {
  children: ReactNode;
}

// Vertical shell. The Header is page furniture; the main column is
// constrained to a comfortable reading measure and breathes in the
// gutters either side.
export function Layout({ children }: LayoutProps) {
  return (
    <div className="min-h-[100dvh] bg-surface text-fg antialiased">
      <Header />
      <main className="max-w-dashboard mx-auto px-4 sm:px-6 lg:px-8 py-6 sm:py-12 pb-[calc(1.5rem+env(safe-area-inset-bottom))]">
        {children}
      </main>
    </div>
  );
}
