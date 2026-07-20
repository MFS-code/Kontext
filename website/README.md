# Website and docs deployment

Marketing site: static files in this directory → **Vercel** → `kontext.run`  
Documentation: [`docs-site/`](../docs-site/) (renders `docs/*.md` + `SPEC.md`) →
**Vercel** → `docs.kontext.run`

Publish the marketing site only after `v0.1.0-alpha.1` (or the current alpha tag)
exists as a GitHub release with `install.yaml`. The page already warns that a
published release is required.

## Vercel marketing (`kontext.run`)

1. Import the `MFS-code/Kontext` repository in the Vercel dashboard.
2. Set **Root Directory** to `website`.
3. Framework preset: **Other** (no build command). Output is the `website/`
   folder as-is. `vercel.json` lives next to `index.html`.
4. Assign the production domain `kontext.run` (and `www` redirect if you want it).

## Vercel docs (`docs.kontext.run`)

1. Create a second Vercel project from the same repository (or use the existing
   team).
2. Root Directory: `docs-site`
3. Build command: `npm run build` · Output: `dist` · Node 20 or 22
4. Assign `docs.kontext.run`
5. DNS: CNAME `docs` → the Vercel DNS target shown for that project

Local:

```bash
cd docs-site
mise exec node@22 -- npm install
mise exec node@22 -- npm run dev
```

Navigation still comes from root [`docs.json`](../docs.json). Markdown stays the
source of truth under `docs/` and `SPEC.md`.

## DNS checklist

| Host | Target | Purpose |
|---|---|---|
| `kontext.run` / `www` | Vercel (`website/`) | Marketing site |
| `docs.kontext.run` | Vercel (`docs-site/`) | Documentation |

## Related policy links

Footer and docs site link to repository policies rather than duplicating them:

- [`SUPPORT.md`](https://github.com/MFS-code/Kontext/blob/main/SUPPORT.md)
- [`SECURITY.md`](https://github.com/MFS-code/Kontext/blob/main/SECURITY.md)
- [`CONTRIBUTING.md`](https://github.com/MFS-code/Kontext/blob/main/CONTRIBUTING.md)
