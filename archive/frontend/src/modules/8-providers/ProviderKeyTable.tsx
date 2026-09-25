import React, { useEffect, useMemo, useState } from 'react';
import type { ProviderKeyRecordDTO } from '@/services/schema';
import {
  ChevronLeft,
  ChevronRight,
  KeyRound,
  Pencil,
  Power,
  PowerOff,
  Trash2,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { toMillis } from '@/lib/datetime';

export interface ProviderKeyTableProps {
  /** Owner of the listed keys. Switching provider resets the page: a page index
   *  from the previous pool means nothing in the new one. */
  providerId: number;
  keys: ProviderKeyRecordDTO[];
  isLoading?: boolean;
  /** Hides the Actions column. Set when the pool is projected from the running
   *  configuration: the rows are read-only there, so offering the buttons would
   *  only produce refused requests. */
  readOnly?: boolean;
  onEdit: (keyRecord: ProviderKeyRecordDTO) => void;
  onDelete: (keyRecord: ProviderKeyRecordDTO) => void;
  onToggleRoutable: (keyRecord: ProviderKeyRecordDTO) => void;
}

/**
 * A credential pool can hold thousands of rows — a harvester syncs one per
 * account — so the table is paginated and its viewport is pinned to a fixed
 * height (`VIEWPORT_CLASS`) instead of growing with the pool. The height is
 * deliberately independent of the page size: the card keeps its footprint and
 * the content below it never shifts, while a page that still overflows scrolls
 * under the sticky header.
 */
const VIEWPORT_CLASS = 'h-[520px]';
const PAGE_SIZES = [25, 50, 100, 250];
const DEFAULT_PAGE_SIZE = 50;

/** Sticky cells need the page's tint painted on them, otherwise rows show
 *  through while scrolling. The divider rides along as an inset shadow because
 *  a collapsed table's `border-b` does not stick with the header. */
const HEADER_CELL_CLASS =
  'sticky top-0 z-10 bg-[#050f1e]/95 backdrop-blur-md py-2.5 px-4 font-normal shadow-[inset_0_-1px_0_rgba(255,255,255,0.06)]';

type PageCell = number | 'gap';

/** Page numbers around the current one, with gaps for the unlisted spans. */
function pageCells(current: number, total: number): PageCell[] {
  const wanted = new Set<number>([1, total, current]);
  if (current - 1 >= 1) wanted.add(current - 1);
  if (current + 1 <= total) wanted.add(current + 1);

  const cells: PageCell[] = [];
  let previous = 0;
  for (const page of [...wanted].sort((a, b) => a - b)) {
    if (previous && page - previous > 1) cells.push('gap');
    cells.push(page);
    previous = page;
  }
  return cells;
}

function statusTone(record: ProviderKeyRecordDTO, expired: boolean) {
  if (expired) return { dot: 'bg-amber-400', text: 'text-amber-400/90' };
  if (record.status === 'active' && record.is_active)
    return { dot: 'bg-emerald-400', text: 'text-emerald-400/90' };
  return { dot: 'bg-neutral-500', text: 'text-neutral-400' };
}

const KeyRow = React.memo(function KeyRow({
  record,
  readOnly,
  onEdit,
  onDelete,
  onToggleRoutable,
}: {
  record: ProviderKeyRecordDTO;
  readOnly: boolean;
  onEdit: (keyRecord: ProviderKeyRecordDTO) => void;
  onDelete: (keyRecord: ProviderKeyRecordDTO) => void;
  onToggleRoutable: (keyRecord: ProviderKeyRecordDTO) => void;
}) {
  const expires = toMillis(record.expires_at);
  const isExpired = expires !== null && expires < Date.now();
  const isRoutable = record.status === 'active' && record.is_active && !isExpired;
  const tone = statusTone(record, isExpired);
  const lastUsed = toMillis(record.last_used_at);

  return (
    <tr className="border-b border-white/[0.04] hover:bg-white/[0.02] transition-colors font-mono text-xs">
      <td className="py-2.5 px-4">
        <div className="flex items-center gap-2">
          <span className="text-[11px] tabular-nums text-neutral-500">#{record.id}</span>
          <span className="text-neutral-200">{record.api_key_hint || '[REDACTED]'}</span>
        </div>
        <span className="text-[10px] text-neutral-600">
          {readOnly ? 'from upstreams.json' : `ref suffix -key-${record.id}`}
        </span>
      </td>

      <td className="py-2.5 px-4">
        <div className="flex items-center gap-1.5 text-[11px] uppercase">
          <span className={cn('w-1.5 h-1.5 rounded-full flex-shrink-0', tone.dot)} />
          <span className={cn('font-medium text-[10px]', tone.text)}>
            {isExpired ? 'expired' : record.status}
          </span>
        </div>
        <span className="text-[10px] text-neutral-500">
          {record.is_active ? 'routing enabled' : 'routing disabled'}
        </span>
      </td>

      <td className="py-2.5 px-4">
        {expires ? (
          <span className={cn('text-[11px]', isExpired ? 'text-rose-400/90' : 'text-neutral-300')}>
            {new Date(expires).toLocaleDateString(undefined, {
              year: 'numeric',
              month: 'short',
              day: 'numeric',
            })}
          </span>
        ) : (
          <span className="text-[11px] text-neutral-500">never</span>
        )}
      </td>

      <td className="py-2.5 px-4 text-neutral-400 tabular-nums text-[11px]">
        {record.total_requests.toLocaleString()}
        <span className="block text-[10px] text-neutral-600">
          {lastUsed ? new Date(lastUsed).toLocaleDateString() : 'never used'}
        </span>
      </td>

      <td className="py-2.5 px-4 text-right">
        {readOnly ? null : (
          <div className="inline-flex items-center gap-1.5">
            <button
              type="button"
              onClick={() => onToggleRoutable(record)}
              className={cn(
                'p-1.5 rounded transition-colors cursor-pointer hover:bg-white/[0.05]',
                isRoutable
                  ? 'text-neutral-400 hover:text-white'
                  : 'text-emerald-400/90 hover:text-emerald-300'
              )}
              title={isRoutable ? 'Take out of rotation' : 'Put back into rotation'}
              aria-label={isRoutable ? 'Deactivate key' : 'Activate key'}
            >
              {isRoutable ? <PowerOff className="w-3.5 h-3.5" /> : <Power className="w-3.5 h-3.5" />}
            </button>

            <button
              type="button"
              onClick={() => onEdit(record)}
              className="p-1.5 rounded text-neutral-400 hover:text-white transition-colors cursor-pointer hover:bg-white/[0.05]"
              title="Edit status, expiry, or rotate the secret"
              aria-label="Edit key"
            >
              <Pencil className="w-3.5 h-3.5" />
            </button>

            <button
              type="button"
              onClick={() => onDelete(record)}
              className="p-1.5 rounded text-neutral-400 hover:text-rose-400 transition-colors cursor-pointer hover:bg-white/[0.05]"
              title="Delete key and its bound credentials"
              aria-label="Delete key"
            >
              <Trash2 className="w-3.5 h-3.5" />
            </button>
          </div>
        )}
      </td>
    </tr>
  );
});

function PageButton({
  page,
  current,
  onSelect,
}: {
  page: PageCell;
  current: number;
  onSelect: (page: number) => void;
}) {
  if (page === 'gap') {
    return <span className="px-1 text-neutral-600 select-none">…</span>;
  }
  const isCurrent = page === current;
  return (
    <button
      type="button"
      onClick={() => onSelect(page)}
      aria-label={`Page ${page}`}
      aria-current={isCurrent ? 'page' : undefined}
      className={cn(
        'min-w-[26px] px-1.5 py-1 rounded tabular-nums transition-colors cursor-pointer',
        isCurrent
          ? 'bg-white/[0.08] text-white'
          : 'text-neutral-400 hover:text-white hover:bg-white/[0.05]'
      )}
    >
      {page}
    </button>
  );
}

function PaginationFooter({
  total,
  page,
  pageCount,
  pageSize,
  onPageChange,
  onPageSizeChange,
}: {
  total: number;
  page: number;
  pageCount: number;
  pageSize: number;
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
}) {
  const first = (page - 1) * pageSize + 1;
  const last = Math.min(page * pageSize, total);

  const navButton =
    'p-1 rounded transition-colors cursor-pointer text-neutral-400 hover:text-white hover:bg-white/[0.05] disabled:opacity-30 disabled:pointer-events-none';

  return (
    <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 px-4 py-2 border-t border-white/[0.06] font-mono text-[10px] text-neutral-500">
      <span className="tabular-nums">
        <span className="text-neutral-300">
          {first.toLocaleString()}–{last.toLocaleString()}
        </span>{' '}
        of {total.toLocaleString()}
      </span>

      <div className="flex items-center gap-4">
        <label className="flex items-center gap-1.5 uppercase tracking-wider cursor-pointer">
          Rows
          <select
            value={pageSize}
            onChange={(e) => onPageSizeChange(Number(e.target.value))}
            aria-label="Rows per page"
            className="px-1.5 py-0.5 rounded bg-transparent border border-white/[0.08] text-neutral-200 font-mono text-[10px] tabular-nums cursor-pointer focus:outline-none focus:border-white/20"
          >
            {PAGE_SIZES.map((size) => (
              <option key={size} value={size} className="bg-[#090b10] text-neutral-200">
                {size}
              </option>
            ))}
          </select>
        </label>

        {pageCount > 1 ? (
          <nav aria-label="Credential pages" className="flex items-center gap-0.5">
            <button
              type="button"
              onClick={() => onPageChange(page - 1)}
              disabled={page <= 1}
              aria-label="Previous page"
              className={navButton}
            >
              <ChevronLeft className="w-3.5 h-3.5" />
            </button>
            {pageCells(page, pageCount).map((cell, index) => (
              <PageButton
                key={cell === 'gap' ? `gap-${index}` : cell}
                page={cell}
                current={page}
                onSelect={onPageChange}
              />
            ))}
            <button
              type="button"
              onClick={() => onPageChange(page + 1)}
              disabled={page >= pageCount}
              aria-label="Next page"
              className={navButton}
            >
              <ChevronRight className="w-3.5 h-3.5" />
            </button>
          </nav>
        ) : null}
      </div>
    </div>
  );
}

export const ProviderKeyTable = React.memo(function ProviderKeyTable({
  providerId,
  keys,
  isLoading = false,
  readOnly = false,
  onEdit,
  onDelete,
  onToggleRoutable,
}: ProviderKeyTableProps) {
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState<number>(DEFAULT_PAGE_SIZE);

  useEffect(() => {
    setPage(1);
  }, [providerId]);

  const pageCount = Math.max(1, Math.ceil(keys.length / pageSize));
  // Clamped rather than stored: deleting keys (or a harvester sync shrinking the
  // pool) can strand an operator on a page that no longer exists, and deriving
  // the rendered page recovers without a second render pass.
  const currentPage = Math.min(page, pageCount);
  const startIndex = (currentPage - 1) * pageSize;
  const pageKeys = useMemo(
    () => keys.slice(startIndex, startIndex + pageSize),
    [keys, startIndex, pageSize]
  );

  const handlePageSizeChange = React.useCallback((nextSize: number) => {
    setPageSize(nextSize);
    setPage(1);
  }, []);

  if (isLoading) {
    return (
      <div className="p-8 text-center text-neutral-500 font-mono text-xs">Loading credentials…</div>
    );
  }

  if (keys.length === 0) {
    return (
      <div className="p-8 text-center flex flex-col items-center justify-center font-mono text-xs text-neutral-500">
        <KeyRound className="w-6 h-6 text-neutral-600 mb-2" />
        No credentials for this provider yet. Add keys here, or let the harvester sync them in.
      </div>
    );
  }

  return (
    <div className="w-full">
      <div className={cn('w-full overflow-auto custom-scrollbar', VIEWPORT_CLASS)}>
        <table className="w-full text-left border-collapse font-mono">
          <thead>
            <tr className="text-[10px] font-mono text-neutral-500 uppercase tracking-wider">
              <th className={HEADER_CELL_CLASS}>Key</th>
              <th className={HEADER_CELL_CLASS}>Status</th>
              <th className={HEADER_CELL_CLASS}>Expires</th>
              <th className={HEADER_CELL_CLASS}>Usage</th>
              {readOnly ? null : <th className={cn(HEADER_CELL_CLASS, 'text-right')}>Actions</th>}
            </tr>
          </thead>
          <tbody>
            {pageKeys.map((record) => (
              <KeyRow
                key={record.id}
                record={record}
                readOnly={readOnly}
                onEdit={onEdit}
                onDelete={onDelete}
                onToggleRoutable={onToggleRoutable}
              />
            ))}
          </tbody>
        </table>
      </div>

      <PaginationFooter
        total={keys.length}
        page={currentPage}
        pageCount={pageCount}
        pageSize={pageSize}
        onPageChange={setPage}
        onPageSizeChange={handlePageSizeChange}
      />
    </div>
  );
});
