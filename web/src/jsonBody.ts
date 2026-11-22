export type ParsedBody = {
  value: unknown;
  format: "json" | "sse" | "text";
  incomplete: boolean;
};

export function parseJsonBody(raw: string, stream = false): ParsedBody {
  try {
    return { value: JSON.parse(raw), format: "json", incomplete: false };
  } catch {
    if (!stream) return { value: raw, format: "text", incomplete: true };
  }

  const events: unknown[] = [];
  let data: string[] = [];
  let incomplete = false;
  let seen = false;
  function finishEvent() {
    if (!data.length) return;
    const text = data.join("\n");
    data = [];
    seen = true;
    if (text.trim() === "[DONE]") return;
    try {
      events.push(JSON.parse(text));
    } catch {
      incomplete = true;
      events.push(text);
    }
  }
  for (const line of raw.replace(/\r\n?/g, "\n").split("\n")) {
    if (!line) finishEvent();
    else if (line.startsWith("data:")) data.push(line.slice(5).replace(/^ /, ""));
  }
  finishEvent();
  return seen
    ? { value: events, format: "sse", incomplete }
    : { value: raw, format: "text", incomplete: true };
}
