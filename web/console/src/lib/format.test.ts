import { describe, expect, it } from "vitest";

import { plural, timeAgo } from "./format";

describe("timeAgo", () => {
  const now = Date.parse("2026-11-12T12:00:00Z");

  it("picks the largest fitting unit", () => {
    expect(timeAgo("2026-11-12T11:59:30Z", now)).toBe("30 sec. ago");
    expect(timeAgo("2026-11-12T09:00:00Z", now)).toBe("3 hr. ago");
    expect(timeAgo("2026-11-10T12:00:00Z", now)).toBe("2 days ago");
  });

  it("does not crash on a bad date", () => {
    expect(timeAgo("not a date", now)).toBe("unknown");
  });
});

describe("plural", () => {
  it("handles singular, plural and -y words", () => {
    expect(plural(1, "e-mail")).toBe("1 e-mail");
    expect(plural(3, "e-mail")).toBe("3 e-mails");
    expect(plural(2, "entry")).toBe("2 entries");
    expect(plural(2, "key")).toBe("2 keys");
    expect(plural(1200, "job")).toBe("1,200 jobs");
  });
});
