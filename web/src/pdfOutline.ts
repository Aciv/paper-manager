// Outline helpers for the PDF reader's navigation pane. They are kept free of
// pdf.js types so the page-mapping rules can be unit tested: the reader passes
// the document's own resolvers in.

export type OutlineEntry = {
  title: string;
  dest: unknown;
  // 1-based page the entry points at, resolved once when the outline loads.
  page?: number;
  items?: OutlineEntry[];
};

// The backend outline persists a flat, ordered list with parent ordinals. It
// carries source pages but no PDF destination, so navigation is page-based.
export type OutlineSectionInput = {
  title: string;
  parentOrdinal: number;
  startPage: number;
};

// Convert the small inline-Markdown subset that can survive in heading text
// into the plain labels expected by the outline. The backend already does this
// for new recognitions; this also repairs persisted outlines created before it.
function plainOutlineTitle(value: string): string {
  let text = value.trim();
  let previous = '';
  while (text && text !== previous) {
    previous = text;
    text = text.replace(/(\*\*|~~)(.+?)\1/g, '$2').trim();
  }
  return text;
}

export function outlineFromSections(sections: readonly OutlineSectionInput[]): OutlineEntry[] {
  const nodes = sections.map((section) => ({
    title: plainOutlineTitle(section.title),
    dest: undefined,
    page: section.startPage > 0 ? section.startPage : undefined,
    items: [] as OutlineEntry[],
  }));
  const roots: OutlineEntry[] = [];
  sections.forEach((section, index) => {
    const parent = section.parentOrdinal >= 0 ? nodes[section.parentOrdinal] : undefined;
    if (parent) parent.items = [...(parent.items ?? []), nodes[index]];
    else roots.push(nodes[index]);
  });
  return roots;
}

type DestinationResolvers = {
  getDestination(dest: unknown): Promise<unknown>;
  getPageIndex(reference: unknown): Promise<number>;
  numPages: number;
};

// A PDF destination is `[pageRef, ...]` where the page reference is either a
// 1-based number or an indirect reference that has to be resolved. Anything
// malformed, out of range or unresolvable is reported as "unknown page".
export async function destinationPage(
  destination: unknown,
  getPageIndex: (reference: unknown) => Promise<number>,
  numPages: number,
): Promise<number | undefined> {
  if (!Array.isArray(destination) || destination.length === 0) return undefined;
  try {
    const reference = destination[0];
    const pageIndex = typeof reference === 'number' ? reference : await getPageIndex(reference);
    const pageNumber = pageIndex + 1;
    return pageNumber >= 1 && pageNumber <= numPages ? pageNumber : undefined;
  } catch {
    return undefined;
  }
}

// Walks the outline once per document load, resolving every entry's page so the
// tree can follow the reader without re-resolving destinations while scrolling.
export async function annotateOutlinePages(nodes: OutlineEntry[], resolvers: DestinationResolvers): Promise<OutlineEntry[]> {
  return Promise.all(nodes.map(async (node) => {
    const named = typeof node.dest === 'string';
    const destination = named ? await resolvers.getDestination(node.dest).catch(() => null) : node.dest;
    const page = await destinationPage(destination, reference => resolvers.getPageIndex(reference), resolvers.numPages);
    const items = node.items?.length ? await annotateOutlinePages(node.items, resolvers) : node.items;
    return { ...node, page, items };
  }));
}

// The section being read: the last entry (in document order, descending into
// children) that starts at or before the current page.
export function activeOutlineNode(nodes: OutlineEntry[], currentPage: number): OutlineEntry | undefined {
  let active: OutlineEntry | undefined;
  const walk = (list: OutlineEntry[]) => {
    for (const node of list) {
      if (typeof node.page === 'number' && node.page <= currentPage) active = node;
      if (node.items?.length) walk(node.items);
    }
  };
  walk(nodes);
  return active;
}
