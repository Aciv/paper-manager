// Reading position and read/unread state live in this browser only. They are
// per-device UI state (not user-authored content), so localStorage is enough:
// a page number per PDF and a read flag. Stored documents are keyed by their
// document id; local files by their folder-relative path and file identity, so
// re-opening the same folder restores the same position.

export type ReadingRecord = {
  page: number;
  read: boolean;
  updatedAt: number;
};

type ReadingStore = Record<string, ReadingRecord>;

const STORAGE_KEY = 'papersManager.reading.v1';
const CHANGE_EVENT = 'papers:reading-state-changed';

function readStore(): ReadingStore {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
    return parsed as ReadingStore;
  } catch {
    return {};
  }
}

function writeStore(store: ReadingStore) {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(store));
  } catch {
    // A full or disabled localStorage must not break reading.
  }
}

function notify() {
  window.dispatchEvent(new Event(CHANGE_EVENT));
}

// Local files have no document id. The picker exposes the folder-relative path;
// size + mtime keep two same-named files apart without hashing the PDF.
export function localFileReadingKey(file: File): string {
  const path = (file as File & { webkitRelativePath?: string }).webkitRelativePath || file.name;
  return `local:${path}:${file.size}:${file.lastModified}`;
}

export function readingRecord(key: string | undefined): ReadingRecord | undefined {
  if (!key) return undefined;
  return readStore()[key];
}

export function isDocumentRead(key: string | undefined): boolean {
  return readingRecord(key)?.read ?? false;
}

// Page saves are silent: only read/unread changes are broadcast, so scrolling a
// PDF never re-renders the catalog and its filters.
export function saveReadingPage(key: string | undefined, page: number) {
  if (!key || !Number.isFinite(page) || page < 1) return;
  const store = readStore();
  const nextPage = Math.round(page);
  const current = store[key];
  if (current?.page === nextPage) return;
  store[key] = { page: nextPage, read: current?.read ?? false, updatedAt: Date.now() };
  writeStore(store);
}

export function setDocumentRead(key: string | undefined, read: boolean, page?: number) {
  if (!key) return;
  const store = readStore();
  const current = store[key];
  const nextPage = page && page >= 1 ? Math.round(page) : current?.page ?? 1;
  if (current?.read === read && current.page === nextPage) return;
  store[key] = { page: nextPage, read, updatedAt: Date.now() };
  writeStore(store);
  notify();
}

// Importing a local PDF turns it into a stored document. Carry the page and
// read flag over once, but never overwrite progress the stored copy already
// has (the user may have opened it from the library before re-importing).
export function transferReadingRecord(fromKey: string | undefined, toKey: string | undefined): ReadingRecord | undefined {
  if (!fromKey || !toKey || fromKey === toKey) return undefined;
  const store = readStore();
  const record = store[fromKey];
  if (!record || store[toKey]) return undefined;
  store[toKey] = { ...record, updatedAt: Date.now() };
  writeStore(store);
  notify();
  return record;
}

export function subscribeReadingState(listener: () => void): () => void {
  const onStorage = (event: StorageEvent) => {
    if (event.key === STORAGE_KEY || event.key === null) listener();
  };
  window.addEventListener(CHANGE_EVENT, listener);
  window.addEventListener('storage', onStorage);
  return () => {
    window.removeEventListener(CHANGE_EVENT, listener);
    window.removeEventListener('storage', onStorage);
  };
}
