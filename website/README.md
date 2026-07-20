# Website and docs deployment

Both public sites are live on Vercel:

- [kontext.run](https://kontext.run) serves the static marketing site from
  this directory.
- [docs.kontext.run](https://docs.kontext.run) serves [`docs-site/`](../docs-site/),
  which renders `docs/*.md` and `SPEC.md`.

## Production configuration

| Site | Root directory | Build | Output |
|---|---|---|---|
| `kontext.run` | `website` | none | static files in `website/` |
| `docs.kontext.run` | `docs-site` | `npm run build` on Node 22 | `dist` |

Local:

```bash
cd docs-site
mise exec node@22 -- npm install
mise exec node@22 -- npm run dev
```

Navigation still comes from root [`docs-nav.json`](../docs-nav.json). Markdown stays the
source of truth under `docs/` and `SPEC.md`.

## Production routing

| Host | Target | Purpose |
|---|---|---|
| `kontext.run` / `www` | Vercel (`website/`) | Marketing site |
| `docs.kontext.run` | Vercel (`docs-site/`) | Documentation |

## Related policy links

Footer and docs site link to repository policies rather than duplicating them:

- [`SUPPORT.md`](https://github.com/MFS-code/Kontext/blob/main/SUPPORT.md)
- [`SECURITY.md`](https://github.com/MFS-code/Kontext/blob/main/SECURITY.md)
- [`CONTRIBUTING.md`](https://github.com/MFS-code/Kontext/blob/main/CONTRIBUTING.md)
