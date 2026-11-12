const relative = new Intl.RelativeTimeFormat("en", { numeric: "auto", style: "short" });
const absolute = new Intl.DateTimeFormat("en-GB", {
  year: "numeric",
  month: "short",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
});
const integer = new Intl.NumberFormat("en");

const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ["day", 86_400],
  ["hour", 3_600],
  ["minute", 60],
  ["second", 1],
];

/** "12 sec. ago", "3 hr. ago"… relative to now. */
export function timeAgo(iso: string, now = Date.now()): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "unknown";
  const seconds = Math.round((date.getTime() - now) / 1000);
  for (const [unit, size] of UNITS) {
    if (Math.abs(seconds) >= size || unit === "second") {
      return relative.format(Math.round(seconds / size), unit);
    }
  }
  return relative.format(0, "second");
}

/** Full local date and time, for titles and details. */
export function dateTime(iso: string): string {
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : absolute.format(date);
}

export function count(n: number): string {
  return integer.format(n);
}

/** "1 e-mail", "3 e-mails", "2 entries". */
export function plural(n: number, word: string): string {
  if (n === 1) return `${count(n)} ${word}`;
  const many = /[^aeiou]y$/.test(word) ? `${word.slice(0, -1)}ies` : `${word}s`;
  return `${count(n)} ${many}`;
}

/**
 * A short form of an e-mail ID. IDs are UUIDv7: they start with a timestamp,
 * so the random end is what tells two recent e-mails apart.
 */
export function shortID(id: string): string {
  return id.length > 13 ? `…${id.slice(-12)}` : id;
}
