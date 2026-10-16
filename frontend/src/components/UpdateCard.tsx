/**
 * Self-update card (`/api/update/*`, admin only).
 *
 * The gateway owns the whole lifecycle — resolve the newest upstream release,
 * refuse hosts that must not self-update (a container, an unwritable install
 * directory), download its own platform's archive, verify the published
 * SHA-256, swap the binary and restart itself. This card only asks and watches.
 *
 * Two truths are kept separate on purpose: whether a newer release exists
 * (update_available / latest) and whether *this* instance could install it
 * (environment.available). A container is told an update exists and told why it
 * cannot apply it, instead of hiding the release.
 */
import { useState } from "react";
import { Download, RefreshCw } from "lucide-react";
import { Badge, type Tone } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { QueryError, QueryLoading } from "@/components/ui/QueryGate";
import {
  useApplyUpdateMutation,
  useUpdateStatusQuery,
  type UpdateInstallDTO,
  type UpdateStatusDTO,
} from "@/services/api";
import { useUiStore } from "@/state/store";

function formatBytes(n?: number): string {
  if (!n || n <= 0) return "—";
  const units = ["B", "KB", "MB", "GB"];
  let value = n;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
}

function formatDate(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString();
}

/** Badge + tone for the card header, from the most severe live condition. */
function headline(
  data: UpdateStatusDTO,
  install?: UpdateInstallDTO,
): { tone: Tone; label: string } {
  if (install?.running) return { tone: "info", label: "UPDATING" };
  if (install?.error) return { tone: "danger", label: "UPDATE FAILED" };
  if (install?.done) return { tone: "ok", label: "INSTALLED" };
  if (data.check_error && !data.latest) {
    return { tone: "warn", label: "CHECK FAILED" };
  }
  if (data.update_available) return { tone: "warn", label: "UPDATE AVAILABLE" };
  return { tone: "ok", label: "UP TO DATE" };
}

function Row({ k, v }: { k: string; v: string }) {
  return (
    <div>
      <span className="k">{k}</span>
      <span className="v" style={{ wordBreak: "break-word" }}>
        {v}
      </span>
    </div>
  );
}

export function UpdateCard() {
  const status = useUpdateStatusQuery();
  const apply = useApplyUpdateMutation();
  const pushToast = useUiStore((s) => s.pushToast);
  const [showNotes, setShowNotes] = useState(false);

  const data = status.data;

  // No data yet: the first read either lands or explains itself. Once a status
  // has been rendered the card keeps showing it through a background refetch
  // failure — which is exactly what a restart looks like from the browser.
  if (!data) {
    if (status.isPending) {
      return <QueryLoading label="Reading update status…" />;
    }
    return <QueryError error={status.error} />;
  }

  const install = data.install;
  const env = data.environment;
  const latest = data.latest;
  const currentVersion = data.current_version;
  const head = headline(data, install);
  const percent = Math.max(0, Math.min(100, install?.percent ?? 0));

  const canApply =
    env.available && data.update_available && !install?.running && !apply.isPending;

  function handleApply() {
    if (!latest) return;
    const confirmed = window.confirm(
      `Install Firefly ${latest.tag} and restart the gateway?\n\n` +
        `Running: ${currentVersion}\n` +
        `Target:   ${latest.tag} (${formatBytes(latest.asset_size)})\n\n` +
        `The download is verified against the release's checksums.txt before the ` +
        `binary is swapped in. Active streams finish first, then the gateway ` +
        `restarts and this dashboard reconnects on its own.`,
    );
    if (!confirmed) return;

    apply.mutate(undefined, {
      onSuccess: (res) => {
        pushToast({
          type: "info",
          title: "Update started",
          message: res.install?.message ?? `Installing ${latest.tag}…`,
        });
      },
      onError: (err: unknown) => {
        pushToast({
          type: "error",
          title: "Update refused",
          message: err instanceof Error ? err.message : String(err),
        });
      },
    });
  }

  return (
    <div className="card">
      <div className="card-header">
        <h2>Update</h2>
        <Badge tone={head.tone}>{head.label}</Badge>
      </div>
      <div className="card-body">
        <div className="kv-list">
          <Row k="Running" v={data.current_version} />
          <Row
            k="Latest release"
            v={latest ? `${latest.tag}${latest.prerelease ? " (pre-release)" : ""}` : "—"}
          />
          <Row k="Platform" v={data.platform} />
          <Row
            k="Update archive"
            v={
              latest?.asset
                ? `${latest.asset} (${formatBytes(latest.asset_size)})`
                : "none published for this platform"
            }
          />
          <Row k="Published" v={formatDate(latest?.published_at)} />
          <Row k="Last checked" v={formatDate(data.checked_at)} />
        </div>

        {/* A stale release is still worth showing: the operator learns a newer
            version exists even when GitHub is refusing to talk to us. */}
        {data.check_error ? (
          <div className="hint" style={{ marginTop: 10, fontSize: 12 }}>
            {data.stale ? "Showing the last known release — " : "Could not reach GitHub — "}
            {data.check_error}
          </div>
        ) : null}

        {!env.available && env.reason ? (
          <div className="hint" style={{ marginTop: 10, fontSize: 12, color: "var(--warn)" }}>
            This instance cannot update itself: {env.reason}
          </div>
        ) : null}

        {showNotes && latest?.notes ? (
          <div
            className="mono"
            style={{
              marginTop: 12,
              maxHeight: 240,
              overflow: "auto",
              fontSize: 12,
              whiteSpace: "pre-wrap",
              color: "var(--muted)",
            }}
          >
            {latest.notes}
          </div>
        ) : null}

        {install?.running ? (
          <div style={{ marginTop: 14 }}>
            <div
              className="progress"
              role="progressbar"
              aria-valuenow={percent}
              aria-valuemin={0}
              aria-valuemax={100}
            >
              <div className="fill" style={{ width: `${percent}%` }} />
            </div>
            <div className="hint" style={{ marginTop: 6, fontSize: 12 }}>
              {install.stage ? `${install.stage}: ` : ""}
              {install.message ?? "working…"}
              {install.total ? ` · ${percent}%` : ""}
            </div>
          </div>
        ) : null}

        {install?.error ? (
          <div className="hint" style={{ marginTop: 10, fontSize: 12, color: "var(--danger)" }}>
            {install.error}
          </div>
        ) : null}

        {install?.done && !install.error ? (
          <div className="hint" style={{ marginTop: 10, fontSize: 12 }}>
            {install.installed} installed
            {install.previous ? ` (was ${install.previous})` : ""}.
            {install.restarting
              ? ` Restarting — ${env.restart_behavior ?? "the gateway is coming back"}.`
              : " Restart the gateway to run it."}
          </div>
        ) : null}

        <div className="form-row form-row-end" style={{ marginTop: 14 }}>
          {latest?.notes ? (
            <Button variant="ghost" onClick={() => setShowNotes((v) => !v)}>
              {showNotes ? "Hide release notes" : "Release notes"}
            </Button>
          ) : null}
          <Button
            variant="ghost"
            leftIcon={<RefreshCw size={14} aria-hidden="true" />}
            onClick={() => void status.refetch()}
            disabled={status.isFetching || install?.running}
          >
            {status.isFetching ? "Checking…" : "Check now"}
          </Button>
          <Button
            variant="primary"
            leftIcon={<Download size={14} aria-hidden="true" />}
            onClick={handleApply}
            disabled={!canApply}
            title={
              !env.available
                ? (env.reason ?? "self-update unavailable")
                : !data.update_available
                  ? "already on the newest release"
                  : undefined
            }
          >
            {apply.isPending || install?.running
              ? "Updating…"
              : latest && data.update_available
                ? `Update to ${latest.tag}`
                : "Up to date"}
          </Button>
        </div>
      </div>
    </div>
  );
}
