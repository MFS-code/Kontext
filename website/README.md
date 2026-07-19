# Website and docs deployment

Marketing site: static files in this directory → **Vercel** → `kontext.run`  
Documentation: repository Markdown + root [`docs.json`](../docs.json) → **Mintlify** → `docs.kontext.run`

Publish the marketing site only after `v0.1.0-alpha.1` (or the current alpha tag)
exists as a GitHub release with `install.yaml`. The page already warns that a
published release is required.

## Vercel (`kontext.run`)

1. Import the `MFS-code/Kontext` repository in the Vercel dashboard.
2. Set **Root Directory** to `website`.
3. Framework preset: **Other** (no build command). Output is the `website/`
   folder as-is. `vercel.json` lives next to `index.html`.
4. Assign the production domain `kontext.run` (and `www` redirect if you want it).
5. Deploy from `main` after the first alpha release assets are live so the
   install URL in `index.html` resolves.

Optional checks after deploy:

- `https://kontext.run/` loads the hero and install block
- `https://kontext.run/favicon.svg` resolves
- `https://kontext.run/robots.txt` and `/sitemap.xml` resolve
- Unknown paths serve `404.html`

## Mintlify (`docs.kontext.run`)

1. Create a Mintlify project and connect the `MFS-code/Kontext` GitHub
   repository (Mintlify GitHub App).
2. Point the docs deployment at branch `main`. Mintlify reads root `docs.json`.
3. In the Mintlify dashboard, add custom domain `docs.kontext.run`.
4. Create the DNS records Mintlify shows (usually a CNAME from
   `docs.kontext.run` to the Mintlify host). Wait for TLS to become active.
5. Confirm logo click goes to `https://kontext.run` and the GitHub navbar link
   opens the repository.

Local preview (optional; requires Node 20 or 22 LTS — Node 25+ is rejected):

```bash
# from the repository root
mise exec node@22 -- npx mint dev
```

Validate configuration:

```bash
mise exec node@22 -- npx mint validate
```

`.mintignore` keeps Kubernetes multi-doc YAML and Go sources out of the Mintlify
build so validate stays green in this monorepo-style layout.

## DNS checklist

| Host | Target | Purpose |
|---|---|---|
| `kontext.run` / `www` | Vercel | Marketing site |
| `docs.kontext.run` | Mintlify (per dashboard) | Documentation |

Domain registration and DNS provider steps stay outside this repository.

## Related policy links

Footer and docs site link to repository policies rather than duplicating them:

- [`SUPPORT.md`](https://github.com/MFS-code/Kontext/blob/main/SUPPORT.md)
- [`SECURITY.md`](https://github.com/MFS-code/Kontext/blob/main/SECURITY.md)
- [`CONTRIBUTING.md`](https://github.com/MFS-code/Kontext/blob/main/CONTRIBUTING.md)

Those files land with the alpha operations documentation PR when it merges to
`main`. Until then the GitHub links 404; prefer GitHub Issues for contact.
