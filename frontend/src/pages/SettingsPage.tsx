import { useMemo, useState, useEffect, type FormEvent } from "react";
import { Badge } from "@/components/ui/Badge";
import { Field, Segmented, SwitchRow } from "@/components/ui/Controls";
import { useAuthStore } from "@/state/auth";
import { useUiStore } from "@/state/store";
import { navigate } from "@/lib/router";
import {
  useSettingsQuery,
  useTunnelStatusQuery,
  useToggleTunnelMutation,
  logoutApi,
  verifyAuthApi,
  updatePasswordApi,
  useTestTursoMutation,
  ApiError,
} from "@/services/api";
import { useDraftStore, useSettingsView } from "@/state/draftStore";
import { handleSessionInvalid } from "@/lib/session";

interface DiffRow {
  kind: "same" | "add" | "del";
  text: string;
}

function buildDiff(a: string, b: string): DiffRow[] {
  const linesA = a.split("\n");
  const linesB = b.split("\n");
  const max = Math.max(linesA.length, linesB.length);
  const out: DiffRow[] = [];

  for (let i = 0; i < max; i++) {
    const la = linesA[i];
    const lb = linesB[i];
    if (la === lb) {
      if (la !== undefined) out.push({ kind: "same", text: la });
    } else {
      if (la !== undefined) out.push({ kind: "del", text: la });
      if (lb !== undefined) out.push({ kind: "add", text: lb });
    }
  }
  return out;
}

function AccessCard() {
  const token = useAuthStore((s) => s.token);
  const clearToken = useAuthStore((s) => s.clearToken);
  const pushToast = useUiStore((s) => s.pushToast);
  const [authenticated, setAuthenticated] = useState<boolean | null>(null);

  useEffect(() => {
    if (!token) {
      setAuthenticated(false);
      return;
    }
    let alive = true;
    verifyAuthApi(token)
      .then((d) => {
        if (!alive) return;
        setAuthenticated(d.authenticated);
        if (!d.authenticated) handleSessionInvalid();
      })
      .catch((err) => {
        if (!alive) return;
        // Only 401 means session rejected. Network drops are not SESSION INVALID.
        if (err instanceof ApiError && err.status === 401) {
          setAuthenticated(false);
          handleSessionInvalid();
          return;
        }
        setAuthenticated(null);
      });
    return () => {
      alive = false;
    };
  }, [token]);

  const statusTone =
    authenticated === true
      ? "ok"
      : authenticated === false && token
        ? "warn"
        : "neutral";
  const statusText =
    token === ""
      ? "NO SESSION"
      : authenticated === null
        ? "CHECKING"
        : authenticated
          ? "AUTHENTICATED"
          : "SESSION INVALID";

  return (
    <div className="card">
      <div className="card-header">
        <h2>Access</h2>
        <Badge tone={statusTone}>{statusText}</Badge>
      </div>
      <div className="card-body">
        <div className="form-row form-row-end">
          <span className="hint">
            {authenticated === true
              ? "Dashboard session active in this browser."
              : authenticated === null && token
                ? "Checking dashboard session…"
                : "No active session. Log in to manage the gateway."}
          </span>
          {authenticated ? (
            <button
              className="btn btn-secondary"
              onClick={async () => {
                try {
                  await logoutApi(token);
                } finally {
                  clearToken();
                  setAuthenticated(false);
                  pushToast({
                    type: "success",
                    title: "Logout",
                    message: "Dashboard session revoked.",
                  });
                  navigate("login");
                }
              }}
            >
              Logout
            </button>
          ) : (
            <button
              className="btn btn-primary"
              onClick={() => navigate("login")}
            >
              Sign in
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

function PasswordCard() {
  const token = useAuthStore((s) => s.token);
  const pushToast = useUiStore((s) => s.pushToast);
  const [currentPass, setCurrentPass] = useState("");
  const [newPass, setNewPass] = useState("");
  const [saving, setSaving] = useState(false);

  async function handleUpdate(e: FormEvent) {
    e.preventDefault();
    if (!currentPass || !newPass || saving) return;
    setSaving(true);
    try {
      await updatePasswordApi(currentPass, newPass, token);
      setCurrentPass("");
      setNewPass("");
      pushToast({
        type: "success",
        title: "Password updated",
        message: "Master password successfully changed.",
      });
    } catch (err) {
      // 401 dead session is handled by handleSessionInvalid (redirects to login).
      // 401 "incorrect current password" remains displayed here.
      if (
        err instanceof ApiError &&
        err.status === 401 &&
        !/incorrect current password/i.test(err.message)
      ) {
        return;
      }
      pushToast({
        type: "error",
        title: "Failed to change password",
        message: err instanceof Error ? err.message : "Unknown error",
      });
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="card">
      <div className="card-header">
        <h2>Password</h2>
        <span className="hint">Change dashboard master password</span>
      </div>
      <div className="card-body">
        <form
          onSubmit={handleUpdate}
          className="stack"
          style={{ marginTop: 0 }}
        >
          <div className="form-grid">
            <Field label="Current password" htmlFor="curr-pass">
              <input
                id="curr-pass"
                type="password"
                value={currentPass}
                placeholder="••••••••"
                onChange={(e) => setCurrentPass(e.target.value)}
                required
              />
            </Field>
            <Field label="New password" htmlFor="new-pass">
              <input
                id="new-pass"
                type="password"
                value={newPass}
                placeholder="••••••••"
                onChange={(e) => setNewPass(e.target.value)}
                required
              />
            </Field>
          </div>
          <div className="form-row form-row-end">
            <span className="hint">
              Changes are saved directly to the backend.
            </span>
            <button
              type="submit"
              className="btn btn-primary"
              disabled={!currentPass || !newPass || saving || !token}
            >
              {saving ? "Saving…" : "Save"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function TokenSaverCard() {
  const settings = useSettingsView();
  const stage = useDraftStore((s) => s.stage);
  const ts = settings.data?.token_saver;

  const savedPrompt = ts?.system_prompt ?? "";
  const [promptDraft, setPromptDraft] = useState<string | null>(null);
  const promptText = promptDraft ?? savedPrompt;
  const promptDirty = promptDraft !== null && promptText !== savedPrompt;

  return (
    <div className="card">
      <div className="card-header">
        <h2>Token Saver</h2>
        <Badge tone={ts?.enabled ? "ok" : "neutral"}>
          {ts?.enabled ? "ENABLED" : "DISABLED"}
        </Badge>
      </div>
      <div className="card-body">
        <SwitchRow
          title="Token Saver — master switch"
          description="Token optimization suite: tool output compression, concise responses, minimal code, and context pruning."
          checked={ts?.enabled ?? false}
          onChange={(v) =>
            settings.data && stage({ token_saver: { ...ts!, enabled: v } })
          }
          ariaLabel="Enable Token Saver"
        />
        <SwitchRow
          title="RTK — tool output compression"
          description="ANSI stripping, log dedup, git diff compact, head/tail truncation."
          checked={ts?.compress_tool_output ?? false}
          onChange={(v) =>
            settings.data &&
            stage({ token_saver: { ...ts!, compress_tool_output: v } })
          }
          ariaLabel="Enable RTK"
        />
        <SwitchRow
          title="Caveman — brevity prompt injection"
          description="Forces terse assistant responses to save output tokens."
          checked={ts?.terse_output ?? false}
          onChange={(v) =>
            settings.data && stage({ token_saver: { ...ts!, terse_output: v } })
          }
          ariaLabel="Enable Caveman"
        />
        <SwitchRow
          title="Ponytail — minimal code bias"
          description="Biases responses toward minimal, patch-style code output."
          checked={ts?.minimal_code ?? false}
          onChange={(v) =>
            settings.data && stage({ token_saver: { ...ts!, minimal_code: v } })
          }
          ariaLabel="Enable Ponytail"
        />
        <SwitchRow
          title="Headroom — middle context pruning"
          description="Prunes middle conversation turns beyond the token budget."
          checked={ts?.compress_context ?? false}
          onChange={(v) =>
            settings.data &&
            stage({ token_saver: { ...ts!, compress_context: v } })
          }
          ariaLabel="Enable Headroom"
        />

        <div
          style={{
            marginTop: 16,
            borderTop: "1px solid var(--border-subtle, rgba(255,255,255,0.06))",
            paddingTop: 14,
          }}
        >
          <Field label="System prompt guard" htmlFor="ts-system-prompt">
            <textarea
              id="ts-system-prompt"
              rows={3}
              spellCheck={false}
              maxLength={4000}
              placeholder="e.g. ENGINEERING MANDATE (Minimal Code): Follow YAGNI. Prefer the standard library over new dependencies. Keep code minimal, direct, and free of needless abstractions."
              value={promptText}
              onChange={(e) => setPromptDraft(e.target.value)}
            />
          </Field>
          <div
            style={{
              display: "flex",
              gap: 8,
              marginTop: 10,
              justifyContent: "flex-end",
              alignItems: "center",
            }}
          >
            <span className="hint" style={{ marginRight: "auto" }}>
              {promptText.length} / 4,000
            </span>
            <button
              type="button"
              className="btn btn-ghost"
              disabled={!promptDirty}
              onClick={() => setPromptDraft(null)}
            >
              Reset
            </button>
            <button
              type="button"
              className="btn btn-primary"
              disabled={!promptDirty || !settings.data}
              onClick={() => {
                if (!settings.data) return;
                stage({
                  token_saver: {
                    ...ts!,
                    system_prompt: promptText.trim(),
                  },
                });
                setPromptDraft(null);
              }}
            >
              Save prompt
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

function TunnelCard() {
  const tunnel = useTunnelStatusQuery();
  const toggle = useToggleTunnelMutation();
  const pushToast = useUiStore((s) => s.pushToast);
  const d = tunnel.data;

  const [mode, setMode] = useState<"quick" | "named">("quick");
  const [token, setToken] = useState("");
  const [showConfig, setShowConfig] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);

  useEffect(() => {
    if (d?.mode === "named") {
      setMode("named");
    }
  }, [d?.mode]);

  // A quick tunnel prints its random trycloudflare.com hostname on stdout, which
  // is what `public_url` carries. A named tunnel never prints one — its hostname
  // comes from the Cloudflare ingress rule — so `public_url` staying empty there
  // is expected and must not be reported as "starting" forever.
  const isNamed = d?.mode === "named";
  const isOnline = !!d?.running && (isNamed || !!d?.public_url);
  const isStarting = !!d?.running && !isNamed && !d?.public_url;
  const isDownloading = !!d?.downloading;
  // The selection can lag the server's live mode only while a Named switch is
  // waiting for its token; every other change reconnects the tunnel in place.
  const modePending = !!d?.running && !!d?.mode && d.mode !== mode;
  const statusTone = isOnline
    ? "ok"
    : isDownloading || isStarting
      ? "warn"
      : "neutral";
  const statusLabel = isOnline
    ? "ONLINE"
    : isDownloading
      ? "DOWNLOADING"
      : isStarting
        ? "STARTING"
        : d?.running
          ? "RUNNING"
          : "STOPPED";

  const copyUrl = () => {
    if (!d?.public_url) return;
    navigator.clipboard.writeText(d.public_url);
    pushToast({ type: "success", title: "URL Copied", message: d.public_url });
  };

  /**
   * A named tunnel cannot start without a credential: either a token already
   * stored on the server (reused automatically) or a freshly pasted one. Reveal
   * the field instead of firing a request the server rejects with
   * `400 token is required`. Shared by the enable switch and the mode switch so
   * both entry points behave identically.
   */
  const namedTokenMissing = (target: "quick" | "named", notify: boolean) => {
    if (target !== "named" || d?.token_configured || token.trim()) return false;
    setShowConfig(true);
    const msg =
      "A named tunnel needs a Cloudflare Tunnel token. Paste it below and switch again — nothing has been changed yet.";
    setErrorMessage(msg);
    if (notify) {
      pushToast({ type: "error", title: "Token Required", message: msg });
    }
    return true;
  };

  const handleToggle = (enabled: boolean) => {
    setErrorMessage(null);
    if (enabled && namedTokenMissing(mode, true)) return;
    toggle.mutate(
      { enabled, mode, token: mode === "named" ? token : undefined },
      {
        onSuccess: (res) => {
          setErrorMessage(null);
          // The dashboard is write-only for the tunnel token: never keep the
          // plaintext in component state once the server has persisted it.
          setToken("");
          if (res.running) {
            pushToast({
              type: "success",
              title: "Tunnel Enabled",
              message:
                res.mode === "quick"
                  ? "Starting quick tunnel (trycloudflare.com)..."
                  : "Starting named tunnel...",
            });
          } else {
            pushToast({
              type: "info",
              title: "Tunnel Disabled",
              message: "Tunnel ingress successfully disabled.",
            });
          }
        },
        onError: (err) => {
          const msg = err instanceof Error ? err.message : "Unknown error";
          setErrorMessage(msg);
          pushToast({
            type: "error",
            title: "Failed to Change Tunnel Status",
            message: msg,
          });
        },
      },
    );
  };

  /**
   * The mode switch is live. Choosing the other mode while the tunnel is running
   * reconnects it right away — `tunnel.Manager.Start` stops the old `cloudflared`
   * process and launches the new one — so the operator never has to toggle the
   * tunnel off, change the mode, and toggle it back on.
   */
  const applyTunnel = (next: "quick" | "named", opts?: { connect?: boolean }) => {
    const previous = mode;
    setMode(next);
    setErrorMessage(null);

    // A token-less Named switch keeps the selection (so the field stays visible)
    // but changes nothing on the server. The toast is only raised on a real
    // change of selection, never on a repeated click of the active mode.
    if (namedTokenMissing(next, previous !== next)) return;

    // While the tunnel is stopped the selection is simply the mode the enable
    // switch (or the button below) will start, so there is nothing to reconnect.
    if (!d?.running && !opts?.connect) return;

    toggle.mutate(
      {
        enabled: true,
        mode: next,
        // Only forward a credential the operator actually typed. The server
        // treats an empty token as "reuse the stored one", so sending it would
        // be pointless noise; omitting it states that intent directly.
        token: next === "named" && token.trim() ? token : undefined,
      },
      {
        onSuccess: (res) => {
          // Write-only credential: never keep the plaintext in component state.
          setToken("");
          pushToast({
            type: "success",
            title: opts?.connect ? "Tunnel Reconnected" : "Tunnel Mode Switched",
            message:
              res.mode === "named"
                ? "Reconnected as a named tunnel."
                : "Reconnected as a quick tunnel (trycloudflare.com)…",
          });
        },
        onError: (err) => {
          const msg = err instanceof Error ? err.message : "Unknown error";
          // The switch must never claim a mode the server did not accept.
          setMode(previous);
          setErrorMessage(msg);
          pushToast({
            type: "error",
            title: "Failed to Switch Tunnel Mode",
            message: msg,
          });
        },
      },
    );
  };

  return (
    <div className="card">
      <div className="card-header">
        <div>
          <h2>Cloudflare Tunnel</h2>
          <span className="hint">
            Native Ingress Engine (remote public access)
          </span>
        </div>
        <Badge tone={statusTone}>{statusLabel}</Badge>
      </div>
      <div className="card-body">
        <SwitchRow
          title="Enable Cloudflare Tunnel"
          description="Enable instant public access via Cloudflare Tunnel for remote AI coding agents without port forwarding."
          checked={d?.running ?? false}
          onChange={handleToggle}
          ariaLabel="Toggle Cloudflare Tunnel"
        />

        <div className="switch-row">
          <div className="info">
            <h3>Tunnel Mode</h3>
            <p>
              <strong>Quick</strong> — instant{" "}
              <code>trycloudflare.com</code> hostname, no configuration needed;
              the hostname is random, changes on every restart, and SSE
              streaming is not supported. <strong>Named</strong> — a stable
              hostname from your Cloudflare ingress rule with streaming enabled;
              it needs a tunnel token.
            </p>
            <p>
              {modePending
                ? `Selected, waiting for a tunnel token — the tunnel is still running in ${(
                    d?.mode ?? ""
                  ).toUpperCase()} mode.`
                : d?.running
                  ? "Switching reconnects the tunnel immediately."
                  : "Applied when the tunnel is enabled."}
            </p>
          </div>
          <Segmented
            items={[
              { id: "quick", label: "Quick" },
              { id: "named", label: "Named" },
            ]}
            value={mode}
            onChange={(id) => applyTunnel(id as "quick" | "named")}
            ariaLabel="Cloudflare Tunnel mode"
          />
        </div>

        {mode === "quick" && (
          <div
            style={{
              marginTop: 12,
              padding: "10px 14px",
              borderRadius: "var(--radius, 6px)",
              background: "rgba(245, 158, 11, 0.08)",
              border: "1px solid rgba(245, 158, 11, 0.3)",
              color: "var(--color-warning, #f59e0b)",
              fontSize: "0.85rem",
              display: "flex",
              flexDirection: "column",
              gap: 4,
            }}
          >
            <strong>Quick tunnel limits</strong>
            <span>
              The <code>trycloudflare.com</code> hostname is random and will
              change on every restart, and streaming (SSE) responses are not
              supported — long-running AI completions will not stream. To pin a
              stable hostname and enable streaming, save a Cloudflare Tunnel
              token and switch to <strong>Named Tunnel</strong>; the saved
              configuration is reused automatically after a restart.
            </span>
          </div>
        )}

        <div className="kv-list" style={{ marginTop: 14 }}>
          <div>
            <span className="k">Operation mode</span>
            <span className="v">
              {d?.mode ? d.mode.toUpperCase() : "DISABLED"}
            </span>
          </div>
          <div>
            <span className="k">Saved configuration</span>
            <span className="v">
              {d?.token_configured
                ? "Named token stored"
                : d?.mode === "named"
                  ? "Named mode, no token"
                  : d?.mode === "quick"
                    ? "Quick (hostname not pinned)"
                    : "—"}
            </span>
          </div>
          <div>
            <span className="k">Process status</span>
            <span className="v">
              {d?.downloading
                ? "Downloading binary…"
                : d?.running
                  ? isStarting
                    ? "Starting…"
                    : "Running"
                  : "Stopped"}
            </span>
          </div>
          <div>
            <span className="k">Target local URL</span>
            <span className="v">{d?.local_url || "—"}</span>
          </div>
          <div>
            <span className="k">Public tunnel URL</span>
            <span className="v">
              {d?.public_url ? (
                <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                  <a
                    href={d.public_url}
                    target="_blank"
                    rel="noreferrer"
                    style={{
                      color: "var(--color-primary, #3b82f6)",
                      textDecoration: "underline",
                      wordBreak: "break-all",
                    }}
                  >
                    {d.public_url}
                  </a>
                  <button
                    type="button"
                    className="btn btn-ghost"
                    style={{
                      padding: "2px 8px",
                      fontSize: "0.75rem",
                      height: "auto",
                    }}
                    onClick={copyUrl}
                  >
                    Copy
                  </button>
                </div>
              ) : isStarting ? (
                <span style={{ color: "var(--color-text-muted, #888)" }}>
                  Generating public URL…
                </span>
              ) : isNamed && d?.running ? (
                <span style={{ color: "var(--color-text-muted, #888)" }}>
                  Defined by your Cloudflare ingress rule — a named tunnel does
                  not announce its hostname.
                </span>
              ) : (
                "—"
              )}
            </span>
          </div>
        </div>

        {d?.downloading && (
          <div
            style={{
              marginTop: 12,
              padding: "10px 14px",
              borderRadius: "var(--radius, 6px)",
              background: "rgba(59, 130, 246, 0.08)",
              border: "1px solid rgba(59, 130, 246, 0.25)",
              color: "var(--color-primary, #3b82f6)",
              fontSize: "0.85rem",
            }}
          >
            Automatically downloading official <code>cloudflared</code> binary
            to the Firefly directory... Please wait a moment.
          </div>
        )}

        {/* Configuration drawer / collapse */}
        <div
          style={{
            marginTop: 14,
            borderTop: "1px solid var(--border-subtle, rgba(255,255,255,0.06))",
            paddingTop: 10,
          }}
        >
          <button
            type="button"
            className="btn btn-ghost btn-sm"
            style={{ marginBottom: 6 }}
            onClick={() => setShowConfig(!showConfig)}
          >
            {showConfig ? "Hide Token Settings" : "Tunnel Token Settings…"}
          </button>

          {showConfig && (
            <>
              <Field
                label="Cloudflare Tunnel Token"
                htmlFor="tunnel-token"
                hint={
                  d?.token_configured
                    ? "Used by named mode only. A token is already stored on the server — leave this empty to reuse it, or paste a new token to replace it."
                    : undefined
                }
              >
                <input
                  id="tunnel-token"
                  type="password"
                  autoComplete="off"
                  spellCheck={false}
                  value={token}
                  placeholder={d?.token_configured ? "•••• (stored)" : "eyJh..."}
                  onChange={(e) => setToken(e.target.value)}
                />
              </Field>

              <div className="form-row form-row-end" style={{ marginTop: 14 }}>
                <button
                  type="button"
                  className="btn btn-primary"
                  disabled={toggle.isPending}
                  onClick={() => applyTunnel(mode, { connect: true })}
                >
                  {toggle.isPending
                    ? "Applying…"
                    : d?.running
                      ? "Apply & Reconnect"
                      : "Save & Enable"}
                </button>
              </div>
            </>
          )}
        </div>

        {errorMessage && (
          <div
            style={{
              marginTop: 12,
              padding: "10px 14px",
              borderRadius: "var(--radius, 6px)",
              background: "rgba(239, 68, 68, 0.1)",
              border: "1px solid rgba(239, 68, 68, 0.3)",
              color: "var(--color-error, #ef4444)",
              fontSize: "0.85rem",
              display: "flex",
              flexDirection: "column",
              gap: 4,
            }}
          >
            <strong>Error:</strong>
            <span
              style={{
                wordBreak: "break-word",
                fontFamily: "var(--font-mono, monospace)",
                fontSize: "0.8rem",
              }}
            >
              {errorMessage}
            </span>
            {errorMessage.toLowerCase().includes("cloudflared") && (
              <span
                style={{
                  color: "var(--color-text-muted, #888)",
                  fontSize: "0.8rem",
                  marginTop: 4,
                }}
              >
                Ensure the <code>cloudflared</code> binary is installed on the
                host server and registered in the system PATH (e.g. download
                from Cloudflare, or via{" "}
                <code>winget install Cloudflare.cloudflared</code> /{" "}
                <code>brew install cloudflared</code>).
              </span>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

function GatewayCard() {
  const settings = useSettingsQuery();
  const replaceAll = useDraftStore((s) => s.replaceAll);
  const pushToast = useUiStore((s) => s.pushToast);
  const [panel, setPanel] = useState("gw-form");
  const [draft, setDraft] = useState<string | null>(null);
  const [diffOpen, setDiffOpen] = useState(false);

  const original = useMemo(
    () => JSON.stringify(settings.data, null, 2),
    [settings.data],
  );
  const text = draft ?? original;

  return (
    <div className="card">
      <div className="card-header">
        <h2>Gateway configuration</h2>
        <Segmented
          items={[
            { id: "gw-form", label: "Overview" },
            { id: "gw-raw", label: "Raw JSON" },
          ]}
          value={panel}
          onChange={setPanel}
          ariaLabel="Gateway editor"
        />
      </div>

      {panel === "gw-form" ? (
        <div className="card-body">
          <div className="kv-list">
            <div>
              <span className="k">Upstreams</span>
              <span className="v">
                {settings.data?.upstreams.length ?? "—"}
              </span>
            </div>
            <div>
              <span className="k">Models</span>
              <span className="v">{settings.data?.models.length ?? "—"}</span>
            </div>
            <div>
              <span className="k">Combos</span>
              <span className="v">{settings.data?.combos?.length ?? "—"}</span>
            </div>
            <div>
              <span className="k">Tenants</span>
              <span className="v">{settings.data?.tenants.length ?? "—"}</span>
            </div>
            <div>
              <span className="k">Authoritative lists</span>
              <span className="v">
                {[
                  settings.data?.manage_upstreams && "upstreams",
                  settings.data?.manage_models && "models",
                  settings.data?.manage_combos && "combos",
                  settings.data?.manage_tenants && "tenants",
                ]
                  .filter(Boolean)
                  .join(", ") || "—"}
              </span>
            </div>
            <div>
              <span className="k">Storage engine</span>
              <span className="v">
                {settings.data?.storage_engine || "file"}
              </span>
            </div>
          </div>
        </div>
      ) : (
        <div className="card-body">
          <Field label="Declarative configuration" htmlFor="gw-raw-json">
            <textarea
              id="gw-raw-json"
              rows={14}
              spellCheck={false}
              value={text}
              onChange={(e) => setDraft(e.target.value)}
            />
          </Field>
          <div
            style={{
              display: "flex",
              gap: 8,
              marginTop: 14,
              justifyContent: "flex-end",
            }}
          >
            <button
              className="btn btn-ghost"
              disabled={draft === null}
              onClick={() => setDraft(null)}
            >
              Reset
            </button>
            <button
              className="btn btn-ghost"
              disabled={draft === null}
              onClick={() => setDiffOpen(true)}
            >
              Diff
            </button>
            <button
              className="btn btn-primary"
              disabled={draft === null}
              onClick={() => {
                try {
                  const parsed = JSON.parse(text) as Parameters<
                    typeof replaceAll
                  >[0];
                  replaceAll(parsed);
                  setDraft(null);
                  pushToast({
                    type: "success",
                    title: "Changes staged",
                    message:
                      "Working copy replaced. Commit from the pending-changes bar.",
                  });
                } catch {
                  pushToast({
                    type: "error",
                    title: "Invalid JSON",
                    message: "Fix syntax errors before saving.",
                  });
                }
              }}
            >
              Apply
            </button>
          </div>
        </div>
      )}

      {diffOpen ? (
        <DiffModal
          original={original}
          draft={text}
          onClose={() => setDiffOpen(false)}
        />
      ) : null}
    </div>
  );
}

function DiffModal({
  original,
  draft,
  onClose,
}: {
  original: string;
  draft: string;
  onClose: () => void;
}) {
  const rows = useMemo(() => buildDiff(original, draft), [original, draft]);
  const changed = rows.filter((r) => r.kind !== "same").length;

  return (
    <div className="modal-backdrop" onClick={onClose} role="presentation">
      <div
        className="modal"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label="Configuration diff"
      >
        <div className="modal-header">
          <h2>Configuration diff</h2>
          <Badge tone={changed === 0 ? "ok" : "warn"}>
            {changed === 0 ? "IDENTICAL" : `${changed} CHANGED LINES`}
          </Badge>
        </div>
        <div className="modal-body">
          <pre className="diff-view">
            {rows.map((r, i) => (
              <div key={i} className={`diff-line ${r.kind}`}>
                <span className="diff-mark">
                  {r.kind === "add" ? "+" : r.kind === "del" ? "-" : " "}
                </span>
                <span>{r.text || " "}</span>
              </div>
            ))}
          </pre>
        </div>
        <div className="modal-footer">
          <button type="button" className="btn btn-secondary" onClick={onClose}>
            Close
          </button>
        </div>
      </div>
    </div>
  );
}

function TursoCard() {
  const settings = useSettingsView();
  const stage = useDraftStore((s) => s.stage);
  const test = useTestTursoMutation();
  const pushToast = useUiStore((s) => s.pushToast);

  const turso = settings.data?.turso;
  const [url, setUrl] = useState(turso?.database_url ?? "");
  const [token, setToken] = useState(turso?.auth_token ?? "");

  useEffect(() => {
    if (turso) {
      setUrl(turso.database_url ?? "");
      setToken(turso.auth_token ?? "");
    }
  }, [turso]);

  async function handleTest() {
    try {
      const res = await test.mutateAsync({
        database_url: url,
        auth_token: token,
      });
      pushToast({
        type: res.ok ? "success" : "error",
        title: res.ok ? "Connection successful" : "Connection failed",
        message:
          res.message ||
          (res.ok
            ? "Turso database is accessible."
            : "Failed to connect to Turso."),
      });
    } catch (err) {
      pushToast({
        type: "error",
        title: "Test failed",
        message: err instanceof Error ? err.message : "Unknown error",
      });
    }
  }

  function handleSave() {
    if (!settings.data) return;
    stage({
      turso: { ...turso, database_url: url, auth_token: token },
    });
  }

  return (
    <div className="card">
      <div className="card-header">
        <h2>Turso Database</h2>
        <Badge tone={turso?.database_url ? "ok" : "neutral"}>
          {turso?.database_url ? "CONFIGURED" : "NOT SET"}
        </Badge>
      </div>
      <div className="card-body">
        <div className="form-grid">
          <Field label="Database URL" htmlFor="turso-url">
            <input
              id="turso-url"
              value={url}
              placeholder="libsql://..."
              onChange={(e) => setUrl(e.target.value)}
            />
          </Field>
          <Field label="Auth token" htmlFor="turso-token">
            <input
              id="turso-token"
              type="password"
              value={token}
              placeholder="••••••••"
              onChange={(e) => setToken(e.target.value)}
            />
          </Field>
        </div>
        <div className="form-row form-row-end" style={{ marginTop: 14 }}>
          <span className="hint">Test connection before saving.</span>
          <div style={{ display: "flex", gap: 8 }}>
            <button
              type="button"
              className="btn btn-secondary"
              disabled={!url.trim() || test.isPending}
              onClick={() => void handleTest()}
            >
              {test.isPending ? "Testing…" : "Test"}
            </button>
            <button
              type="button"
              className="btn btn-primary"
              disabled={!url.trim()}
              onClick={handleSave}
            >
              Save
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

function AutoTLSCard() {
  const settings = useSettingsView();
  const stage = useDraftStore((s) => s.stage);
  const tls = settings.data?.auto_tls;
  const [domain, setDomain] = useState(tls?.domain ?? "");
  const [email, setEmail] = useState(tls?.email ?? "");
  const [enabled, setEnabled] = useState(tls?.enabled ?? false);

  useEffect(() => {
    if (tls) {
      setDomain(tls.domain ?? "");
      setEmail(tls.email ?? "");
      setEnabled(tls.enabled);
    }
  }, [tls]);

  function handleSave() {
    if (!settings.data) return;
    stage({ auto_tls: { enabled, domain, email } });
  }

  return (
    <div className="card">
      <div className="card-header">
        <h2>AutoTLS</h2>
        <Badge tone={enabled ? "ok" : "neutral"}>
          {enabled ? "ENABLED" : "DISABLED"}
        </Badge>
      </div>
      <div className="card-body">
        <div className="form-grid">
          <Field label="Public domain" htmlFor="tls-domain">
            <input
              id="tls-domain"
              value={domain}
              placeholder="gateway.example.com"
              onChange={(e) => setDomain(e.target.value)}
            />
          </Field>
          <Field label="ACME email" htmlFor="tls-email">
            <input
              id="tls-email"
              type="email"
              value={email}
              placeholder="admin@example.com"
              onChange={(e) => setEmail(e.target.value)}
            />
          </Field>
        </div>
        <div className="form-row form-row-end" style={{ marginTop: 14 }}>
          <label
            style={{
              display: "flex",
              alignItems: "center",
              gap: 8,
              cursor: "pointer",
            }}
          >
            <input
              type="checkbox"
              checked={enabled}
              onChange={(e) => setEnabled(e.target.checked)}
            />
            <span>Enable AutoTLS</span>
          </label>
          <button
            type="button"
            className="btn btn-primary"
            onClick={handleSave}
          >
            Save
          </button>
        </div>
      </div>
    </div>
  );
}

export function SettingsPage() {
  return (
    <div className="page-col">
      <div className="page-header">
        <div>
          <h1>Settings</h1>
          <p>Global gateway configuration, one domain per card.</p>
        </div>
      </div>

      <div className="stack">
        <AccessCard />
        <PasswordCard />
        <TokenSaverCard />
        <TunnelCard />

        <TursoCard />
        <AutoTLSCard />
        <GatewayCard />
      </div>
    </div>
  );
}
