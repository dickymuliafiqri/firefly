import { useEffect, useState } from 'react';

/**
 * Hash router ringan — pola registry.tsx (WORK_PLAN §3).
 * Dashboard embedded single-binary; tidak butuh react-router.
 *
 * Format hash: `#/<page>` atau `#/<page>/<rest>?key=value`.
 * `page` adalah segmen pertama; `rest` adalah sisa path (mis. `edit/openai-main`).
 */
export function parseHash(): { path: string; rest: string; query: URLSearchParams } {
  const raw = (window.location.hash || '').replace(/^#\/?/, '');
  const [head, qs] = raw.split('?');
  const [first, ...tail] = (head ?? '').split('/').filter(Boolean);
  return { path: first ?? '', rest: tail.join('/'), query: new URLSearchParams(qs ?? '') };
}

/** Alias hash → pageId (mis. `upstream` → `upstream-editor`). */
const PATH_ALIAS: Record<string, string> = { upstream: 'upstream-editor' };

export function parsePage<P extends string>(valid: readonly P[], fallback: P): P {
  const { path } = parseHash();
  const mapped = PATH_ALIAS[path] ?? path;
  return (valid as readonly string[]).includes(mapped) ? (mapped as P) : fallback;
}

export function useHashPage<P extends string>(valid: readonly P[], fallback: P): P {
  const [page, setPage] = useState<P>(() => parsePage(valid, fallback));

  useEffect(() => {
    const onHashChange = () => setPage(parsePage(valid, fallback));
    window.addEventListener('hashchange', onHashChange);
    return () => window.removeEventListener('hashchange', onHashChange);
  }, [valid, fallback]);

  return page;
}

/** Sisa path setelah segmen halaman, reaktif terhadap hashchange. */
export function useHashRest(): string {
  const [rest, setRest] = useState(() => parseHash().rest);
  useEffect(() => {
    const onHashChange = () => setRest(parseHash().rest);
    window.addEventListener('hashchange', onHashChange);
    return () => window.removeEventListener('hashchange', onHashChange);
  }, []);
  return rest;
}

export function navigate(id: string, query?: Record<string, string>) {
  const qs = query && Object.keys(query).length > 0 ? '?' + new URLSearchParams(query).toString() : '';
  window.location.hash = '#/' + id + qs;
}
