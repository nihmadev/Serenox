---
title: Contributing to documentation
description: Run, edit, validate, localize, and version the Serenox docs.
---

# Contributing to documentation

Canonical English content lives in `website/content/docs`. From `website`, run `npm ci` and `npm run dev`; validate with `npm run check` and `npm run build`.

Use Markdown for prose and MDX only when a Fumadocs component improves comprehension. Every page requires English `title` and `description` frontmatter, sentence-case headings, stable slugs, copyable examples, and valid rendered/source links. Product changes update guides, references, and compatibility matrices in the same pull request.

## Localization strategy

English is the source locale and stays unprefixed at `/docs/...`. Future complete locales use `/<locale>/docs/...`; IDs and slugs stay locale-independent. Locale identity is separate from release identity. Do not publish a selector until a second locale is complete. Missing translations fall back to the complete English route at navigation level; pages never mix languages.

Translated content will use a parallel locale tree, preserve heading IDs, and record the English page ID and revision. Images containing text require localized alternatives and native-language review.

## Release-version strategy

Current English stays `/docs/...`; a future snapshot uses `/docs/v1/...`, and translated snapshots use `/<locale>/docs/v1/...`. A snapshot copies a reviewed tree at release time, emits its own canonicals and sitemap entries, accepts supported-release corrections, and is eventually archived. No fake selector or empty archive is published now.

## Deployment

Vercel is primary. Set the project root to `website`, build with `npm run build`, and set `NEXT_PUBLIC_SITE_URL` to the deployment origin. Production uses `docs.serenox.dev`; DNS is an operator action. Previews need no Telegram or repository secrets. Roll back by promoting the previous successful deployment.
