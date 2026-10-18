import { ChevronLeft, ChevronRight } from 'lucide-react';

export interface PaginationProps {
  /** Total number of rows across all pages. */
  total: number;
  /** 1-based current page (already clamped by the caller). */
  page: number;
  /** Rows per page. */
  pageSize: number;
  /** Page count (ceil(total / pageSize), at least 1). */
  totalPages: number;
  /** Moves to a 1-based page. */
  onPageChange: (page: number) => void;
  /** Singular noun for the row count, e.g. "key" → "Showing 1–15 of 30 keys". */
  unit?: string;
}

/**
 * Pagination renders the "Showing X–Y of Z" bar with prev/next controls used
 * for every bounded list in the dashboard (WARP pool slots, sing-box nodes,
 * upstream keys). It renders nothing when the list fits on one page, so a short
 * list is never burdened with a dormant control strip.
 */
export function Pagination({
  total,
  page,
  pageSize,
  totalPages,
  onPageChange,
  unit = 'item',
}: PaginationProps) {
  if (total <= pageSize) return null;
  const startIdx = total === 0 ? 0 : (page - 1) * pageSize + 1;
  const endIdx = Math.min(page * pageSize, total);

  return (
    <div
      className="flex items-center justify-between pt-3 border-t border-[var(--line)] text-xs text-faint mt-2"
      style={{ flexWrap: 'wrap', gap: 8 }}
    >
      <span className="tabular-nums">
        Showing <span className="text-ink font-medium">{startIdx}–{endIdx}</span> of{' '}
        <span className="text-ink font-medium">{total}</span> {unit}
        {total === 1 ? '' : 's'}
      </span>
      <div className="flex items-center gap-1.5">
        <button
          type="button"
          className="pagination-btn"
          disabled={page <= 1}
          onClick={() => onPageChange(Math.max(1, page - 1))}
          aria-label="Previous page"
        >
          <ChevronLeft className="w-4 h-4" />
        </button>
        <span className="text-xs text-faint px-1.5 tabular-nums">
          {page} / {totalPages}
        </span>
        <button
          type="button"
          className="pagination-btn"
          disabled={page >= totalPages}
          onClick={() => onPageChange(Math.min(totalPages, page + 1))}
          aria-label="Next page"
        >
          <ChevronRight className="w-4 h-4" />
        </button>
      </div>
    </div>
  );
}
