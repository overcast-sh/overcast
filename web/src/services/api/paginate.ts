/**
 * Every item of a paginated listing: drains an SDK paginator
 * (`paginateListWorkGroups(…)` and the like) and keeps each page's items.
 */
export async function collectPages<Page, Item>(
  pages: AsyncIterable<Page>,
  itemsOf: (page: Page) => Item[] | undefined,
): Promise<Item[]> {
  const items: Item[] = []
  for await (const page of pages) items.push(...(itemsOf(page) ?? []))
  return items
}
