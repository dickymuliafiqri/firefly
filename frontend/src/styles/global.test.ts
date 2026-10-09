/// <reference types="vite/client" />
import css from "./global.css?raw";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

/**
 * `.mono`, `.dim`, and `.faint` used to exist only as `td.dim` / `td.faint` and
 * `.resmon .mono`, so every span, input, p, and div carrying them silently fell
 * back to the inherited font and colour. Around fifty call sites across the
 * dashboard were written expecting these utilities to work on any element —
 * including the model-id sub-line in the pricing catalog picker, which had to
 * fall back to an inline `fontFamily` because no class did the job.
 *
 * These tests inject the real stylesheet into jsdom and read back computed
 * styles, so a future tidy-up that re-scopes a utility fails here instead of
 * quietly reverting fifty call sites to their inherited defaults.
 */
describe("global.css text utilities", () => {
  let style: HTMLStyleElement;

  beforeEach(() => {
    style = document.createElement("style");
    style.textContent = css;
    document.head.appendChild(style);
  });

  afterEach(() => {
    style.remove();
  });

  function computed(className: string): CSSStyleDeclaration {
    const el = document.createElement("span");
    el.className = className;
    el.textContent = "probe";
    document.body.appendChild(el);
    const cs = getComputedStyle(el);
    el.remove();
    return cs;
  }

  it("applies a monospace face and tabular figures to .mono on any element", () => {
    const cs = computed("mono");
    expect(cs.fontFamily).toContain("JetBrains Mono");
    expect(cs.fontFamily).toContain("ui-monospace");
    expect(cs.fontVariantNumeric).toBe("tabular-nums");
  });

  it("applies the muted token to .dim outside a table cell", () => {
    expect(computed("dim").color).toBe("var(--muted)");
  });

  it("applies the faint token to .faint outside a table cell", () => {
    expect(computed("faint").color).toBe("var(--faint)");
  });

  it("leaves an unclassed element on the inherited defaults", () => {
    const cs = computed("");
    expect(cs.fontFamily).not.toContain("JetBrains Mono");
    expect(cs.color).not.toBe("var(--muted)");
    expect(cs.color).not.toBe("var(--faint)");
  });

  it("still scopes the waterfall and visualizer dim rules", () => {
    // A bare `.dim` inside these containers must not pick up their fill or
    // background overrides, which is why those rules stay compound.
    expect(css).toMatch(/^\.waterfall span\.dim\s*\{/m);
    expect(css).toMatch(/^\.visualizer-svg \.viz-base\.dim\s*\{/m);
    expect(css).toMatch(/^\.visualizer-svg \.viz-pkt\.dim\s*\{/m);
    expect(css).toMatch(/^\.visualizer-svg \.viz-st\.dim\s*\{/m);
  });
});
