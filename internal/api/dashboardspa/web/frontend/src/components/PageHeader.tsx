import type { ReactNode } from 'react';

// The opener of every route. A Display heading naming the view, a
// one-line synopsis of state, and an optional right-aligned meta slot
// (SSE indicator, refresh control, etc.). Sets the rhythm for the
// page: generous space above + below, no card, no border.

interface PageHeaderProps {
  title: string;
  synopsis?: ReactNode;
  meta?: ReactNode;
  /**
   * One compact control pinned to the top right, on the title's own row, at
   * every width. `meta` drops below the synopsis on a phone; this never does,
   * so it adds no height and never pushes the page down.
   */
  action?: ReactNode;
  className?: string;
}

// Two columns at every width: the title takes the first, `action` the second
// on row one. A phone gives the synopsis and meta the full width below. From
// md up, meta moves into the right column beside the synopsis, bottom-aligned
// with it, which is where it always sat.
//
// Place items with col-start and col-end only. A col-span class is the
// grid-column shorthand, so `md:col-span-1` also resets the start to auto,
// and the synopsis then jumps into the empty right column on a page with no
// action.
export function PageHeader({ title, synopsis, meta, action, className = '' }: PageHeaderProps) {
  const metaRow = synopsis || action ? 'md:row-start-2' : 'md:row-start-1';
  // With no action, a phone leaves the right column empty, so it gets no gap
  // either and the title keeps the full width.
  const gapX = action ? 'gap-x-6' : 'md:gap-x-6';
  return (
    <header
      className={`grid grid-cols-[minmax(0,1fr)_auto] items-start ${gapX} mb-10 ${className}`}
    >
      <h1 className="col-start-1 row-start-1 min-w-0 text-display font-semibold tracking-tighter text-fg leading-[1.05]">
        {title}
      </h1>
      {action && <div className="col-start-2 row-start-1 self-center">{action}</div>}
      {synopsis && (
        <p className="col-start-1 col-end-3 mt-2 text-body text-fg-muted max-w-prose md:col-end-2">
          {synopsis}
        </p>
      )}
      {meta && (
        <div
          className={`col-start-1 col-end-3 mt-4 flex flex-wrap items-center gap-4 text-label uppercase tracking-wider md:col-start-2 md:mt-0 md:self-end md:justify-end ${metaRow}`}
        >
          {meta}
        </div>
      )}
    </header>
  );
}
