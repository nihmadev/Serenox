import { RootProvider } from "fumadocs-ui/provider/next";
import "./global.css";
import type { Metadata } from "next";

export const metadata: Metadata = {
  metadataBase: new URL(
    process.env.NEXT_PUBLIC_SITE_URL ?? "https://docs.serenox.dev",
  ),
  title: { default: "Serenox documentation", template: "%s | Serenox" },
  description:
    "Operate Serenox safely and build bounded Go and Python plugins.",
  icons: {
    icon: [
      {
        url: "/brand/serenox-mark-black.svg",
        type: "image/svg+xml",
        media: "(prefers-color-scheme: light)",
      },
      {
        url: "/brand/serenox-mark-white.svg",
        type: "image/svg+xml",
        media: "(prefers-color-scheme: dark)",
      },
    ],
    apple: {
      url: "/brand/serenox-icon-black-1024.png",
      type: "image/png",
      sizes: "1024x1024",
    },
  },
};

export default function Layout({ children }: LayoutProps<"/">) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body className="flex flex-col min-h-screen">
        <RootProvider>{children}</RootProvider>
      </body>
    </html>
  );
}
