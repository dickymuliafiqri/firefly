import { useMemo, useState } from 'react';
import { Download, RefreshCw } from 'lucide-react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Badge } from '@/components/ui/Badge';
import { Button } from '@/components/ui/Button';
import { fetchPricingCatalog, importPricing, refreshPricingCatalog } from '@/services/api';
import { useUiStore } from '@/state/store';

/**
 * The models.dev side of the Pricing page: browse the public catalog, pick
 * models, and seed them into the local sheet. Seeding is once-only by design —
 * the backend never overwrites a key the sheet already holds — so the preview
 * says how many entries would be added and how many are already present
 * rather than promising a rewrite.
 */
export function PricingCatalogPanel() {
  const qc = useQueryClient();
  const pushToast = useUiStore((s) => s.pushToast);

  const [provider, setProvider] = useState('all');
  const [search, setSearch] = useState('');
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [open, setOpen] = useState(false);

  const catalog = useQuery({
    queryKey: ['pricing', 'catalog'],
    queryFn: fetchPricingCatalog,
    enabled: open,
  });

  const refresh = useMutation({
    mutationFn: () => refreshPricingCatalog(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['pricing', 'catalog'] });
      pushToast({ type: 'success', title: 'Catalog refreshed' });
    },
    onError: (err: unknown) =>
      pushToast({
        type: 'error',
        title: 'Refresh failed',
        message: err instanceof Error ? err.message : String(err),
      }),
  });

  const seed = useMutation({
    mutationFn: (models: string[]) => importPricing({ models }),
    onSuccess: (res) => {
      setSelected(new Set());
      void qc.invalidateQueries({ queryKey: ['pricing'] });
      pushToast({
        type: 'success',
        title: 'Import complete',
        message: `${res.added} added, ${res.skipped} already present`,
      });
    },
    onError: (err: unknown) =>
      pushToast({
        type: 'error',
        title: 'Import failed',
        message: err instanceof Error ? err.message : String(err),
      }),
  });

  const entries = catalog.data?.entries ?? [];
  const providers = useMemo(
    () => Array.from(new Set(entries.map((e) => e.provider))).sort(),
    [entries],
  );

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase();
    return entries.filter(
      (e) =>
        (provider === 'all' || e.provider === provider) &&
        (q === '' ||
          e.key.toLowerCase().includes(q) ||
          e.model_id.toLowerCase().includes(q) ||
          e.name.toLowerCase().includes(q)),
    );
  }, [entries, provider, search]);

  const toggle = (key: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  return (
    <div className="card">
      <div className="card-header">
        <h2>models.dev catalog</h2>
        <div style={{ display: 'flex', gap: 8 }}>
          <Button
            leftIcon={<RefreshCw size={14} />}
            onClick={() => refresh.mutate()}
            disabled={refresh.isPending}
          >
            {refresh.isPending ? 'Refreshing…' : 'Refresh'}
          </Button>
          <Button
            variant="primary"
            leftIcon={<Download size={14} />}
            onClick={() => setOpen((v) => !v)}
          >
            {open ? 'Close browser' : 'Browse & import'}
          </Button>
        </div>
      </div>

      {open ? (
        <>
          <div className="card-body" style={{ paddingBottom: 0 }}>
            <div className="filter-bar" style={{ marginBottom: 12 }}>
              <input
                type="search"
                placeholder="Search catalog models…"
                aria-label="Search catalog models"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
              <select
                aria-label="Filter by provider"
                value={provider}
                onChange={(e) => setProvider(e.target.value)}
              >
                <option value="all">All providers</option>
                {providers.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            </div>
          </div>

          <div className="card-body tight table-wrap">
            <table>
              <thead>
                <tr>
                  <th />
                  <th>Model</th>
                  <th>Provider</th>
                  <th className="num">Input / 1M</th>
                  <th className="num">Output / 1M</th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((e) => (
                  <tr key={e.key}>
                    <td>
                      <input
                        type="checkbox"
                        aria-label={`Select ${e.key}`}
                        checked={selected.has(e.key)}
                        onChange={() => toggle(e.key)}
                      />
                    </td>
                    <td>
                      <span>{e.name || e.model_id}</span>
                      {e.model_id && e.model_id !== e.name ? (
                        <span
                          style={{
                            display: 'block',
                            fontSize: 11,
                            marginTop: 1,
                            color: 'var(--faint)',
                            fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                          }}
                        >
                          {e.model_id}
                        </span>
                      ) : null}
                    </td>
                    <td className="dim">{e.provider}</td>
                    <td className="num">${(e.input_micros_per_m / 1_000_000).toFixed(3)}</td>
                    <td className="num">${(e.output_micros_per_m / 1_000_000).toFixed(3)}</td>
                  </tr>
                ))}
                {filtered.length === 0 ? (
                  <tr>
                    <td colSpan={5} className="faint">
                      {catalog.isLoading
                        ? 'Loading the models.dev catalog…'
                        : 'No catalog models match the filter.'}
                    </td>
                  </tr>
                ) : null}
              </tbody>
            </table>
          </div>

          <div className="card-body">
            <div style={{ display: 'flex', gap: 10, alignItems: 'center' }}>
              <Badge tone={selected.size > 0 ? 'info' : 'neutral'}>
                {selected.size} SELECTED
              </Badge>
              <span className="spacer" />
              <Button
                variant="primary"
                disabled={selected.size === 0 || seed.isPending}
                onClick={() => seed.mutate(Array.from(selected))}
              >
                {seed.isPending ? 'Importing…' : `Import ${selected.size} model${selected.size === 1 ? '' : 's'}`}
              </Button>
            </div>
            <p className="hint" style={{ marginTop: 8, fontSize: 12, color: 'var(--faint)' }}>
              Seeding is once-only: a key the local sheet already holds — manual, wildcard, or seeded
              by an earlier import — is never overwritten, so re-running this only adds models the
              sheet does not know yet.
            </p>
          </div>
        </>
      ) : (
        <div className="card-body">
          <p className="hint" style={{ fontSize: 12, color: 'var(--faint)' }}>
            Seed the local sheet from the public models.dev catalog. The catalog is cached in memory
            and refreshed on demand; only models whose provider maps to a configured protocol are
            offered.
          </p>
        </div>
      )}
    </div>
  );
}
