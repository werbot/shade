export interface Span { start: number; end: number; type: string; rule: string }
export interface Segment { text: string; span?: Span }

/** Splits text at span boundaries; offsets are UTF-16 units, as in String.slice. */
export function segments(text: string, spans: Span[]): Segment[] {
  // U+2400 stands in for NUL, which the page cannot show. It is one UTF-16 unit as well, so the
  // offsets of the spans stay valid.
  const shown = text.replaceAll("\0", "␀");
  const out: Segment[] = [];
  let cursor = 0;
  for (const span of spans) {
    // One pass: a span before the cursor is out of order or overlaps the previous one, and
    // slicing it would repeat text. Skipping a span leaves the cursor where it was.
    if (span.start < cursor || span.end > shown.length || span.start >= span.end) continue;
    if (span.start > cursor) out.push({ text: shown.slice(cursor, span.start) });
    out.push({ text: shown.slice(span.start, span.end), span });
    cursor = span.end;
  }
  if (cursor < shown.length) out.push({ text: shown.slice(cursor) });
  return out;
}
