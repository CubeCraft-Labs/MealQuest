export type PlaceholderPageProps = {
  // The route's screen name, shown as the page heading.
  title: string
}

// Stands in for a screen until its feature is built.
export function PlaceholderPage({ title }: PlaceholderPageProps) {
  return <h1>{title}</h1>
}
