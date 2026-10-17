# Serenox documentation website

The production documentation portal uses Next.js App Router, Fumadocs,
TypeScript, and MDX. Canonical English content lives in `content/docs`.

Install deterministically and run the development server:

```sh
npm ci
npm run dev
```

Open <http://localhost:3000>; the root redirects to `/docs`.

Validate and build:

```sh
npm run check
npm run build
```

## Layout

In the project, you can see:

- `content/docs`: hand-authored canonical content and navigation metadata.
- `app/docs`: documentation layout and page rendering.
- `app/api/search`: self-hostable full-text search over public page content.
- `app/llms.txt` and `app/llms-full.txt`: machine-readable public docs.
- `scripts`: content, link, anchor, route, language, and secret checks.

| Route                     | Description                                            |
| ------------------------- | ------------------------------------------------------ |
| `app/(home)`              | Redirects the root to the documentation.               |
| `app/docs`                | The documentation layout and pages.                    |
| `app/api/search/route.ts` | The Route Handler for search.                          |

## Deployment

Vercel is primary: set the project root to `website`, keep `npm ci` and
`npm run build`, and set `NEXT_PUBLIC_SITE_URL` to the deployment origin.
Production uses `https://docs.serenox.dev`; preview builds require no secrets.
