import { describe, expect, it } from "vitest";

import { summarizeError } from "./errors";

describe("summarizeError", () => {
  it("names the usual causes", () => {
    expect(
      summarizeError(
        'invalid template: render body.html: template: body.html:1:108: executing "content" at <index .tracking "number">: error calling index',
      ),
    ).toBe("Template error");
    expect(summarizeError("smtp: 550 5.1.1 mailbox unavailable")).toBe("Rejected by the provider");
    expect(summarizeError("smtp: 421 try again later")).toBe("Provider busy");
    expect(summarizeError("ses: context deadline exceeded")).toBe("Provider timeout");
    expect(summarizeError("something else")).toBe("Delivery error");
  });
});
