function parseFrontmatter(raw: string): {
  data: Record<string, unknown>;
  content: string;
} {
  if (!raw.startsWith("---\n") && raw !== "---") {
    return { data: {}, content: raw };
  }
  const end = raw.indexOf("\n---\n", 4);
  if (end === -1) {
    return { data: {}, content: raw };
  }
  const block = raw.slice(4, end);
  const content = raw.slice(end + 5);
  const data: Record<string, unknown> = {};
  for (const line of block.split("\n")) {
    const idx = line.indexOf(":");
    if (idx === -1) {
      continue;
    }
    const key = line.slice(0, idx).trim();
    let value = line.slice(idx + 1).trim();
    if (
      (value.startsWith('"') && value.endsWith('"')) ||
      (value.startsWith("'") && value.endsWith("'"))
    ) {
      value = value.slice(1, -1);
    }
    data[key] = value;
  }
  return { data, content };
}

export { parseFrontmatter };
