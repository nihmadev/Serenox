import type { MetadataRoute } from "next";
import { source } from "@/lib/source";

const origin = process.env.NEXT_PUBLIC_SITE_URL ?? "https://docs.serenox.dev";

export default function sitemap(): MetadataRoute.Sitemap {
  return source.getPages().map((page) => ({
    url: new URL(page.url, origin).toString(),
    changeFrequency: "weekly",
    priority: page.url === "/docs" ? 1 : 0.7,
  }));
}
