import { c, mono } from "../theme";

interface Token {
  text: string;
  color: string;
}

// tokenizeJSON produces colored spans for a pretty-printed JSON value, matching
// the prototype's highlighting.
function tokenizeJSON(obj: unknown): Token[] {
  const str = JSON.stringify(obj, null, 2) ?? "null";
  const tokens: Token[] = [];
  const regex =
    /("(?:\\.|[^"\\])*")(\s*:)?|\b(true|false|null)\b|(-?\d+\.?\d*(?:[eE][+-]?\d+)?)|([{}[\],])/g;
  let lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = regex.exec(str)) !== null) {
    if (m.index > lastIndex) tokens.push({ text: str.slice(lastIndex, m.index), color: c.text2 });
    if (m[1]) {
      tokens.push({ text: m[1], color: m[2] ? "#93c5fd" : "#86efac" });
      if (m[2]) tokens.push({ text: m[2], color: c.muted2 });
    } else if (m[3]) {
      tokens.push({ text: m[0], color: c.accent });
    } else if (m[4]) {
      tokens.push({ text: m[0], color: c.warn });
    } else if (m[5]) {
      tokens.push({ text: m[0], color: c.muted2 });
    }
    lastIndex = regex.lastIndex;
  }
  if (lastIndex < str.length) tokens.push({ text: str.slice(lastIndex), color: c.text2 });
  return tokens;
}

export function PayloadView({ value }: { value: unknown }) {
  const tokens = tokenizeJSON(value);
  return (
    <div
      style={{
        marginTop: 12,
        background: c.bg,
        border: `1px solid ${c.border}`,
        borderRadius: 8,
        padding: "14px 16px",
        fontFamily: mono,
        fontSize: 12.5,
        whiteSpace: "pre-wrap",
        lineHeight: 1.6,
        overflowX: "auto",
      }}
    >
      {tokens.map((t, i) => (
        <span key={i} style={{ color: t.color }}>
          {t.text}
        </span>
      ))}
    </div>
  );
}
