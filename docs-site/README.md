# Docs site (`docs.kontext.run`)

Self-hosted documentation app (ViaBOM-style). Renders repository Markdown from
`docs/*.md` and `SPEC.md`, with navigation from root `docs.json`. Visual
language matches [`website/`](../website/).

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

DNS at Namecheap (or your registrar):

```text
A    docs    76.76.21.21
```

(Or the CNAME target shown in the Vercel domain panel.)

Production alias today: https://kontext-docs-red.vercel.app

## LLM / raw Markdown

- `/llms.txt` — page index
- `/llms-full.txt` — concatenated bodies
- `/raw/docs/*.md`, `/raw/SPEC.md` — source mirrors
