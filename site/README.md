# site — the public site and docs

A Next.js app, statically exported. It is the source for two things:

| Where | How |
| --- | --- |
| https://rapidnative.github.io/orchd | `deploy/publish-docs.sh` (or the `Publish site` GitHub Action on push to `main`) publishes `out/` to the `gh-pages` branch |
| the box's root page | `deploy/deploy.sh` builds with `SITE_BASE_PATH=` and syncs `out/` to the server |

```bash
npm install
npm run dev                       # http://localhost:3000/orchd
SITE_BASE_PATH= npm run build     # export at the domain root -> out/
```

`basePath` defaults to `/orchd` (a GitHub Pages project site lives under the repo name) and
is overridden with `SITE_BASE_PATH` for root-served deployments.

## Layout

```
app/
  page.tsx              the landing page
  layout.tsx            header, footer, theme bootstrap
  globals.css           every colour token and all prose styling
  docs/
    layout.tsx          sidebar + prose container + prev/next
    page.mdx            docs home
    <slug>/page.mdx     one page per doc
components/
  nav.ts                THE docs tree — sidebar order and prev/next come from here
```

Adding a page: create `app/docs/<slug>/page.mdx` with an `export const metadata`, then add
it to `DOC_GROUPS` in `components/nav.ts`. Nothing else is wired by hand.

Headings get ids and anchor links automatically (`rehype-slug` +
`rehype-autolink-headings`), and GitHub-flavoured markdown (tables, strikethrough) is on.
