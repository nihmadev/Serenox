import Link from "next/link";

export default function NotFound() {
  return (
    <main className="mx-auto flex min-h-[70vh] max-w-xl flex-col justify-center px-6">
      <p className="font-mono text-sm text-fd-muted-foreground">404</p>
      <h1 className="mt-3 text-3xl font-semibold">
        Documentation page not found
      </h1>
      <p className="mt-4 text-fd-muted-foreground">
        The page may have moved or the link may be incorrect.
      </p>
      <Link
        className="mt-6 w-fit rounded-md border px-4 py-2 font-medium"
        href="/docs"
      >
        Return to documentation
      </Link>
    </main>
  );
}
