import { useEffect, useMemo, useState } from 'react';
import { Coins, Trash2 } from 'lucide-react';
import { Badge, type Tone } from '@/components/ui/Badge';
import { Button } from '@/components/ui/Button';
import { Drawer } from '@/components/ui/Drawer';
import { KpiCard, KpiGrid, PageHeader } from '@/components/ui/PageHeader';
import { QueryGate } from '@/components/ui/QueryGate';
import { Segmented } from '@/components/ui/Controls';
import { deletePricingEntry, fetchPricing, upsertPricingEntry } from '@/services/api';
import { PricingCatalogPanel } from '@/components/PricingCatalogPanel';
import type { PricingEntryDTO, PricingEntryView } from '@/services/schema';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';

/**
 * Prices are stored as integer micro-USD per 1M tokens, which is numerically
 * identical to a provider's USD-per-1M figure. Rendering divides by a million,
 * so the page shows exactly the number on the provider's pricing page.
 */
function perM(micros: number | undefined): string {
  if (micros === undefined || micros === null) return '—';
  return `$${(micros / 1_000_000).toFixed(3)}`;
}

function microsFromUSD(text: string): number {
  const v = Number.parseFloat(text);
  if (!Number.isFinite(v) || v < 0) return 0;
  return Math.round(v * 1_000_000);
}

function usdFromMicros(micros: number | undefined): string {
  if (micros === undefined || micros === null) return '';
  return (micros / 1_000_000).toFixed(3);
}

const SOURCE_TONE: Record<string, Tone> = {
  manual: 'warn',
  'models.dev': 'ok',
};

type SourceFilter = 'all' | 'manual' | 'models.dev';
type StatusFilter = 'all' | 'used' | 'unused';

export function PricingPage() {
  const qc = useQueryClient();
  const pricing = useQuery({ queryKey: ['pricing'], queryFn: fetchPricing });

  const [search, setSearch] = useState('');
  const [source, setSource] = useState<SourceFilter>('all');
  const [status, setStatus] = useState<StatusFilter>('all');
  const [editing, setEditing] = useState<PricingEntryView | null>(null);
  const [draftKey, setDraftKey] = useState('');
  const [draft, setDraft] = useState({ input: '', output: '', cacheRead: '', cacheWrite: '' });
  const [formError, setFormError] = useState<string | null>(null);

  const rows = pricing.data?.entries ?? [];

  const filtered = useMemo(
    () =>
      rows.filter(
        (r) =>
          (source === 'all' || (r.source ?? 'manual') === source) &&
          (status === 'all' || (status === 'used' ? r.used : !r.used)) &&
          (search === '' || r.model.toLowerCase().includes(search.toLowerCase())),
      ),
    [rows, source, status, search],
  );

  const invalidate = () => qc.invalidateQueries({ queryKey: ['pricing'] });

  const save = useMutation({
    mutationFn: (payload: { key: string; entry: Omit<PricingEntryDTO, 'model'> }) =>
      upsertPricingEntry(payload.key, payload.entry),
    onSuccess: () => {
      setEditing(null);
      setFormError(null);
      void invalidate();
    },
    onError: (err: unknown) => setFormError(err instanceof Error ? err.message : String(err)),
  });

  const remove = useMutation({
    mutationFn: (key: string) => deletePricingEntry(key),
    onSuccess: () => void invalidate(),
  });

  // A "Set price" jump from the Models page carries the model key in the hash
  // query, so the drawer opens pre-filled instead of asking for it again.
  const [prefill, setPrefill] = useState<string | null>(null);
  useEffect(() => {
    const read = () => {
      const qs = window.location.hash.split('?')[1] ?? '';
      setPrefill(new URLSearchParams(qs).get('model'));
    };
    read();
    window.addEventListener('hashchange', read);
    return () => window.removeEventListener('hashchange', read);
  }, []);

  const openNew = () => {
    setEditing(null);
    setDraftKey(prefill ?? '');
    setDraft({ input: '', output: '', cacheRead: '', cacheWrite: '' });
    setFormError(null);
    setDrafting(true);
  };

  const openEdit = (entry: PricingEntryView) => {
    setEditing(entry);
    setDraftKey(entry.model);
    setDraft({
      input: usdFromMicros(entry.input_micros_per_m),
      output: usdFromMicros(entry.output_micros_per_m),
      cacheRead: usdFromMicros(entry.cache_read_micros_per_m),
      cacheWrite: usdFromMicros(entry.cache_write_micros_per_m),
    });
    setFormError(null);
    setDrafting(true);
  };

  const [drafting, setDrafting] = useState(false);

  const submit = () => {
    const key = draftKey.trim();
    if (key === '') {
      setFormError('A model key is required.');
      return;
    }
    if (draft.input.trim() === '' || draft.output.trim() === '') {
      setFormError('Input and output prices are required.');
      return;
    }
    save.mutate({
      key,
      entry: {
        input_micros_per_m: microsFromUSD(draft.input),
        output_micros_per_m: microsFromUSD(draft.output),
        ...(draft.cacheRead.trim() !== ''
          ? { cache_read_micros_per_m: microsFromUSD(draft.cacheRead) }
          : {}),
        ...(draft.cacheWrite.trim() !== ''
          ? { cache_write_micros_per_m: microsFromUSD(draft.cacheWrite) }
          : {}),
      },
    });
  };

  const usedCount = rows.filter((r) => r.used).length;
  const manualCount = rows.filter((r) => (r.source ?? 'manual') === 'manual').length;

  return (
    <div className="page-col">
      <PageHeader
        title="Pricing"
        description="Model price sheet driving every cost figure in the gateway. Prices are USD per 1M tokens."
        actions={
          <Button leftIcon={<Coins size={14} />} onClick={openNew}>
            Set price
          </Button>
        }
      />

      <KpiGrid>
        <KpiCard label="Registered models" value={rows.length.toLocaleString()} />
        <KpiCard label="Used by catalog" value={usedCount.toLocaleString()} />
        <KpiCard label="Manual overrides" value={manualCount.toLocaleString()} />
        <KpiCard
          label="Unregistered"
          value={(rows.length - usedCount).toLocaleString()}
          tone={rows.length - usedCount > 0 ? 'warn' : 'plain'}
        />
      </KpiGrid>

      <div className="filter-bar">
        <input
          type="search"
          placeholder="Search model key…"
          aria-label="Search model key"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        <Segmented
          ariaLabel="Filter by source"
          value={source}
          onChange={(v) => setSource(v as SourceFilter)}
          items={[
            { id: 'all', label: 'All sources' },
            { id: 'manual', label: 'Manual' },
            { id: 'models.dev', label: 'models.dev' },
          ]}
        />
        <Segmented
          ariaLabel="Filter by usage"
          value={status}
          onChange={(v) => setStatus(v as StatusFilter)}
          items={[
            { id: 'all', label: 'All' },
            { id: 'used', label: 'Used' },
            { id: 'unused', label: 'Unused' },
          ]}
        />
      </div>

      <QueryGate isLoading={pricing.isLoading} error={pricing.error}>
        <div className="card">
          <div className="card-body tight table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Model key</th>
                  <th className="num">Input / 1M</th>
                  <th className="num">Output / 1M</th>
                  <th className="num">Cache read</th>
                  <th className="num">Cache write</th>
                  <th>Source</th>
                  <th>Status</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {filtered.map((r) => (
                  <tr key={r.model}>
                    <td className="mono">{r.model}</td>
                    <td className="num">{perM(r.input_micros_per_m)}</td>
                    <td className="num">{perM(r.output_micros_per_m)}</td>
                    <td className="num">
                      {r.cache_read_micros_per_m ? perM(r.cache_read_micros_per_m) : '—'}
                    </td>
                    <td className="num">
                      {r.cache_write_micros_per_m ? perM(r.cache_write_micros_per_m) : '—'}
                    </td>
                    <td>
                      <Badge tone={SOURCE_TONE[r.source ?? 'manual'] ?? 'neutral'}>
                        {r.source ?? 'manual'}
                      </Badge>
                    </td>
                    <td>
                      {r.used ? <Badge tone="ok">used</Badge> : <Badge tone="neutral">unused</Badge>}
                    </td>
                    <td className="row-actions">
                      <Button variant="ghost" onClick={() => openEdit(r)}>
                        Edit
                      </Button>
                      <Button
                        variant="ghost"
                        aria-label={`Delete price for ${r.model}`}
                        onClick={() => {
                          if (window.confirm(`Delete the price entry for ${r.model}?`)) {
                            remove.mutate(r.model);
                          }
                        }}
                      >
                        <Trash2 size={13} />
                      </Button>
                    </td>
                  </tr>
                ))}
                {filtered.length === 0 ? (
                  <tr>
                    <td colSpan={8} className="faint">
                      No price entries match the filter.
                    </td>
                  </tr>
                ) : null}
              </tbody>
            </table>
          </div>
        </div>
        <p className="hint" style={{ marginTop: 10, fontSize: 12, color: 'var(--faint)' }}>
          A model with no entry falls back to the legacy flat rate, so an unpriced model still bills
          something. Editing an entry marks it <strong>manual</strong>, which a later models.dev import
          never overwrites.
        </p>
      </QueryGate>

      <PricingCatalogPanel />

      <Drawer open={drafting} onClose={() => setDrafting(false)} title={editing ? `Edit ${editing.model}` : 'Set price'}>
        <div className="stack">
          <label className="field">
            <span>Model key</span>
            <input
              value={draftKey}
              onChange={(e) => setDraftKey(e.target.value)}
              placeholder="gpt-4o or claude-sonnet-4-*"
              disabled={editing !== null}
            />
            <em>Exact model name, or a wildcard prefix ending in *.</em>
          </label>
          <label className="field">
            <span>Input price / 1M tokens (USD)</span>
            <input
              value={draft.input}
              inputMode="decimal"
              onChange={(e) => setDraft({ ...draft, input: e.target.value })}
              placeholder="2.500"
            />
          </label>
          <label className="field">
            <span>Output price / 1M tokens (USD)</span>
            <input
              value={draft.output}
              inputMode="decimal"
              onChange={(e) => setDraft({ ...draft, output: e.target.value })}
              placeholder="10.000"
            />
          </label>
          <label className="field">
            <span>Cache read / 1M tokens (USD, optional)</span>
            <input
              value={draft.cacheRead}
              inputMode="decimal"
              onChange={(e) => setDraft({ ...draft, cacheRead: e.target.value })}
              placeholder="1.250"
            />
            <em>Blank bills cached tokens at the input price.</em>
          </label>
          <label className="field">
            <span>Cache write / 1M tokens (USD, optional)</span>
            <input
              value={draft.cacheWrite}
              inputMode="decimal"
              onChange={(e) => setDraft({ ...draft, cacheWrite: e.target.value })}
              placeholder="3.125"
            />
          </label>
          {formError ? <p className="form-error">{formError}</p> : null}
          <div className="row-actions end">
            <Button variant="ghost" onClick={() => setDrafting(false)}>
              Cancel
            </Button>
            <Button onClick={submit} disabled={save.isPending}>
              {save.isPending ? 'Saving…' : 'Save price'}
            </Button>
          </div>
        </div>
      </Drawer>
    </div>
  );
}
