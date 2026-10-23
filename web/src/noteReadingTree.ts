import type { ReadingNoteSectionEntry } from './api.ts';

export type NoteTreeNode = { entry: ReadingNoteSectionEntry; number: string; children: NoteTreeNode[] };

export function buildNoteTree(entries: ReadingNoteSectionEntry[]): NoteTreeNode[] {
  const roots: NoteTreeNode[] = [];
  const stack: NoteTreeNode[] = [];
  for (const entry of [...entries].sort((a, b) => a.order - b.order)) {
    while (stack.length && stack[stack.length - 1].entry.level >= entry.level) stack.pop();
    // A preamble is never a parent.
    if (stack[0]?.entry.level === 0) stack.length = 0;
    const parent = stack[stack.length - 1];
    const siblings = parent ? parent.children : roots;
    const node: NoteTreeNode = {
      entry, number: `${parent ? `${parent.number}.` : ''}${siblings.length + 1}`, children: [],
    };
    siblings.push(node);
    stack.push(node);
  }
  return roots;
}

// Reading a descendant keeps its containing H1 open; the selected child is an
// anchor within that chapter. Legacy notes without an H1 use their root subtree.
export function getReadingChapter(entries: ReadingNoteSectionEntry[], selectedId?: string): ReadingNoteSectionEntry[] {
  const roots = buildNoteTree(entries);
  const flatten = (node: NoteTreeNode): ReadingNoteSectionEntry[] => [node.entry, ...node.children.flatMap(flatten)];
  for (const root of roots) {
    const chapter = flatten(root);
    if (chapter.some((entry) => entry.id === selectedId)) return chapter;
  }
  return [];
}
