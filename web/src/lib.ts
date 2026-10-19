import { NO_VALUE } from "@warehouse/ui-kit";

/** "2026-10-09 08:05:30Z" for an RFC 3339 instant; the raw text when unparseable. */
export function utcLabel(iso: string | undefined): string {
  if (!iso) return NO_VALUE;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const d = new Date(t).toISOString();
  return `${d.slice(0, 10)} ${d.slice(11, 19)}Z`;
}

/** Metres with one decimal: "4.5 m". */
export function metres(m: number): string {
  return `${m.toFixed(1)} m`;
}

/** The id rule shared by site and station ids: 1..64 characters, no
 *  whitespace, control characters or "/". */
export function idProblem(what: string, id: string): string | null {
  if (id.length === 0) return `Enter a ${what} id.`;
  if (id.length > 64) return `A ${what} id is at most 64 characters.`;
  // eslint-disable-next-line no-control-regex
  if (/[\s/\u0000-\u001f\u007f]/.test(id)) return `A ${what} id cannot contain spaces, control characters or '/'.`;
  return null;
}

/** Like idProblem, but an empty filter is fine ("any"). */
export function optionalIdProblem(what: string, id: string): string | null {
  return id.length === 0 ? null : idProblem(what, id);
}

/** A finite number typed into a field, or null when the text is empty or not a number. */
export function finiteNumber(raw: string): number | null {
  const s = raw.trim();
  if (s === "") return null;
  const n = Number(s);
  return Number.isFinite(n) ? n : null;
}

/** A strictly whole number typed into a field, or null ("12.5", "1e3", "abc", ""). */
export function wholeNumber(raw: string): number | null {
  const s = raw.trim();
  if (!/^\d+$/.test(s)) return null;
  const n = Number(s);
  return Number.isSafeInteger(n) ? n : null;
}

/** The largest hintTtlSeconds the contract accepts (int32). */
export const MAX_HINT_TTL_SECONDS = 2147483647;
