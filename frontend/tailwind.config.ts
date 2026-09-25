import type { Config } from "tailwindcss";

/**
 * Design tokens — sumber nilai ada di src/styles/global.css (CSS variables).
 * Nilai identik dengan DESIGN.md §2.
 */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        "sky-top": "var(--sky-top)",
        "sky-mid": "var(--sky-mid)",
        "surface-page": "var(--surface-page)",
        "surface-card": "var(--surface-card)",
        "surface-raised": "var(--surface-raised)",
        "surface-input": "var(--surface-input)",
        "surface-overlay": "var(--surface-overlay)",
        ink: "var(--ink)",
        muted: "var(--muted)",
        faint: "var(--faint)",
        biolum: "var(--biolum)",
        "biolum-bright": "var(--biolum-bright)",
        "biolum-ink": "var(--biolum-ink)",
        ok: "var(--ok)",
        warn: "var(--warn)",
        danger: "var(--danger)",
        info: "var(--info)",
        line: "var(--line)",
        "line-strong": "var(--line-strong)",
      },
      fontFamily: {
        display: ["Fraunces", "Georgia", "serif"],
        sans: ["Inter", "system-ui", "sans-serif"],
        mono: [
          '"JetBrains Mono"',
          "ui-monospace",
          "Menlo",
          "Consolas",
          "monospace",
        ],
      },
      borderRadius: {
        card: "10px",
        panel: "12px",
      },
    },
  },
  plugins: [],
} satisfies Config;
