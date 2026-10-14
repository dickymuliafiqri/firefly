import { describe, expect, it } from "vitest";
import {
  findRegisteredRoute,
  isValidModelName,
  sanitizeModelName,
  uniquePublicName,
  type CatalogModelLike,
} from "@/services/schema";

const catalog = (
  entries: Array<[string, string, string]>,
): CatalogModelLike[] =>
  entries.map(([public_name, upstream, upstream_model]) => ({
    public_name,
    upstream,
    upstream_model,
  }));

describe("findRegisteredRoute", () => {
  it("matches the same upstream model id only on its own upstream", () => {
    const models = catalog([
      ["gpt-4o", "upstream-a", "gpt-4o"],
      ["upstream-b--gpt-4o", "upstream-b", "gpt-4o"],
    ]);

    expect(findRegisteredRoute(models, "upstream-a", "gpt-4o")?.public_name).toBe(
      "gpt-4o",
    );
    expect(
      findRegisteredRoute(models, "upstream-b", "gpt-4o")?.public_name,
    ).toBe("upstream-b--gpt-4o");
    expect(findRegisteredRoute(models, "upstream-c", "gpt-4o")).toBeUndefined();
  });

  it("falls back to a public_name match for legacy entries", () => {
    const models = catalog([["gpt-4o", "upstream-a", ""]]);
    expect(findRegisteredRoute(models, "upstream-a", "gpt-4o")).toBeDefined();
  });

  it("does not treat another upstream's name as registered here", () => {
    const models = catalog([["gpt-4o", "upstream-a", "gpt-4o"]]);
    expect(findRegisteredRoute(models, "upstream-b", "gpt-4o")).toBeUndefined();
  });
});

describe("uniquePublicName", () => {
  it("uses the sanitized id when it is free", () => {
    expect(uniquePublicName("gpt-4o", "upstream-a", [])).toBe("gpt-4o");
  });

  it("namespaces with the upstream when the id is taken elsewhere", () => {
    const models = catalog([["gpt-4o", "upstream-a", "gpt-4o"]]);
    expect(uniquePublicName("gpt-4o", "upstream-b", models)).toBe(
      "upstream-b--gpt-4o",
    );
  });

  it("sanitizes an id that the gateway would reject", () => {
    expect(uniquePublicName("anthropic/claude-3-5", "upstream-a", [])).toBe(
      "anthropic-claude-3-5",
    );
  });

  it("appends a numeric suffix when the namespaced name is also taken", () => {
    const models = catalog([
      ["gpt-4o", "upstream-a", "gpt-4o"],
      ["upstream-b--gpt-4o", "upstream-b", "gpt-4o"],
      ["upstream-c--gpt-4o", "upstream-c", "gpt-4o"],
    ]);
    expect(uniquePublicName("gpt-4o", "upstream-b", models)).toBe(
      "upstream-b--gpt-4o-2",
    );
  });

  it("is deterministic across repeated calls", () => {
    const models = catalog([["gpt-4o", "upstream-a", "gpt-4o"]]);
    const first = uniquePublicName("gpt-4o", "upstream-b", models);
    const second = uniquePublicName("gpt-4o", "upstream-b", models);
    expect(first).toBe(second);
  });

  it("keeps the result within the 128-char budget and valid", () => {
    const long = "x".repeat(300);
    const name = uniquePublicName(long, "a".repeat(200), []);
    expect(name.length).toBeLessThanOrEqual(128);
    expect(isValidModelName(name)).toBe(true);
  });

  it("truncates the model part, never the upstream namespace", () => {
    const models = catalog([["m".repeat(128), "upstream-a", "m".repeat(128)]]);
    const name = uniquePublicName("m".repeat(128), "upstream-b", models);
    expect(name.startsWith("upstream-b--")).toBe(true);
    expect(name.length).toBeLessThanOrEqual(128);
  });

  it("returns empty when nothing nameable remains", () => {
    expect(uniquePublicName("///", "upstream-a", [])).toBe("");
  });

  it("stays valid for idempotent sanitization", () => {
    expect(sanitizeModelName(uniquePublicName("a/b:c", "up", []))).toBe(
      uniquePublicName("a/b:c", "up", []),
    );
  });
});
