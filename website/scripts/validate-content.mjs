import { existsSync, readdirSync, readFileSync } from "node:fs";
import { dirname, extname, join, resolve } from "node:path";

const root = resolve("content/docs");
const files = [];

function walk(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) walk(path);
    else if ([".md", ".mdx"].includes(extname(path))) files.push(path);
  }
}

function slug(value) {
  return value
    .toLowerCase()
    .trim()
    .replace(/[`*_~]/g, "")
    .replace(/[^\p{L}\p{N}\s-]/gu, "")
    .replace(/\s+/g, "-")
    .replace(/-+/g, "-");
}

walk(root);
const routes = new Set(
  files.map((file) => {
    const relative = file
      .slice(root.length + 1)
      .replace(/\.(md|mdx)$/, "")
      .replace(/\/index$/, "");
    return `/docs${relative ? `/${relative}` : ""}`;
  }),
);
const errors = [];

for (const file of files) {
  const text = readFileSync(file, "utf8");
  const frontmatter = text.match(/^---\n([\s\S]*?)\n---\n/)?.[1] ?? "";
  if (
    !/^title:\s*\S/m.test(frontmatter) ||
    !/^description:\s*\S/m.test(frontmatter)
  ) {
    errors.push(`${file}: missing title/description frontmatter`);
  }
  const headings = new Set(
    [...text.matchAll(/^#{1,6}\s+(.+)$/gm)].map((match) => slug(match[1])),
  );
  for (const match of text.matchAll(/\[[^\]]*\]\(([^)]+)\)/g)) {
    const href = match[1].split(/\s+["']/)[0];
    if (/^(https?:|mailto:|#)/.test(href)) continue;
    const [target, anchor] = href.split("#");
    if (target.startsWith("/docs")) {
      if (!routes.has(target)) errors.push(`${file}: unknown route ${target}`);
      continue;
    }
    if (!target) continue;
    const absolute = resolve(dirname(file), decodeURIComponent(target));
    if (!existsSync(absolute)) {
      errors.push(`${file}: missing link target ${target}`);
      continue;
    }
    if (anchor && [".md", ".mdx"].includes(extname(absolute))) {
      const targetText = readFileSync(absolute, "utf8");
      const targetHeadings = new Set(
        [...targetText.matchAll(/^#{1,6}\s+(.+)$/gm)].map((heading) =>
          slug(heading[1]),
        ),
      );
      if (!targetHeadings.has(anchor))
        errors.push(`${file}: missing anchor ${target}#${anchor}`);
    }
  }
  if (
    /[/\\]home[/\\]|SERENOX_TELEGRAM_API_HASH\s*=\s*[0-9a-f]{20,}/i.test(text)
  ) {
    errors.push(`${file}: possible local path or credential value`);
  }
  if ([...text].some((character) => /[А-Яа-яЁё]/.test(character))) {
    errors.push(`${file}: non-English Cyrillic content`);
  }
  void headings;
}

if (errors.length) {
  console.error(errors.join("\n"));
  process.exit(1);
}
console.log(`Validated ${files.length} documentation pages.`);
