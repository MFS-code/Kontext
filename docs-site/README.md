# Docs site (`docs.kontext.run`)

Self-hosted documentation app (ViaBOM-style). Renders repository Markdown from
`docs/*.md` and `SPEC.md`, with navigation from root `docs-nav.json`. Visual
language matches [`website/`](../website/). The production site is live at
[docs.kontext.run](https://docs.kontext.run).

`npm run sync` copies those sources into `docs-site/content/` (gitignored) before
dev/build. On Vercel the full repo is available, so sync works with Root
Directory `docs-site`.

## Local

```bash
cd docs-site
mise exec node@22 -- npm install
mise exec node@22 -- npm run dev
```

```bash
mise exec node@22 -- npm run build
mise exec node@22 -- npm run preview
```

## Vercel project `kontext-docs`

| Setting | Value |
|---|---|
| Root Directory | `docs-site` |
| Build | `npm run build` |
| Output | `dist` |
| Node | 22.x |
| Domain | `docs.kontext.run` |

Production DNS:

```text
A    docs    76.76.21.21
```

## LLM / raw Markdown

- `/llms.txt` — page index
- `/llms-full.txt` — concatenated bodies
- `/raw/docs/*.md`, `/raw/SPEC.md` — source mirrors
