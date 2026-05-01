import type { FileIdentity, UploadSessionRecord } from "./types";

const DB_NAME = "vod_uploads";
const STORE = "sessions";
const DB_VERSION = 1;
export const SESSION_TTL_MS = 24 * 60 * 60 * 1000;

export interface SessionStore {
  put(record: UploadSessionRecord): Promise<void>;
  get(uploadId: string): Promise<UploadSessionRecord | undefined>;
  list(): Promise<UploadSessionRecord[]>;
  delete(uploadId: string): Promise<void>;
  clear(): Promise<void>;
}

export function fileMatches(record: FileIdentity, file: FileIdentity): boolean {
  return (
    record.name === file.name &&
    record.size === file.size &&
    record.lastModified === file.lastModified
  );
}

export function isExpired(record: UploadSessionRecord, now: number = Date.now()): boolean {
  return now - record.createdAt > SESSION_TTL_MS;
}

export function createMemorySessionStore(): SessionStore {
  const map = new Map<string, UploadSessionRecord>();
  return {
    async put(record) {
      map.set(record.uploadId, { ...record });
    },
    async get(uploadId) {
      const r = map.get(uploadId);
      return r ? { ...r } : undefined;
    },
    async list() {
      return [...map.values()].map((r) => ({ ...r }));
    },
    async delete(uploadId) {
      map.delete(uploadId);
    },
    async clear() {
      map.clear();
    },
  };
}

function openDB(factory: IDBFactory): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = factory.open(DB_NAME, DB_VERSION);
    req.onupgradeneeded = () => {
      const db = req.result;
      if (!db.objectStoreNames.contains(STORE)) {
        db.createObjectStore(STORE, { keyPath: "uploadId" });
      }
    };
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

function tx<T>(
  db: IDBDatabase,
  mode: IDBTransactionMode,
  fn: (store: IDBObjectStore) => IDBRequest<T>
): Promise<T> {
  return new Promise((resolve, reject) => {
    const t = db.transaction(STORE, mode);
    const store = t.objectStore(STORE);
    const req = fn(store);
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

export function createIndexedDBSessionStore(
  factory: IDBFactory = globalThis.indexedDB
): SessionStore {
  if (!factory) {
    return createMemorySessionStore();
  }

  const dbPromise = openDB(factory);

  return {
    async put(record) {
      const db = await dbPromise;
      await tx(db, "readwrite", (s) => s.put(record));
    },
    async get(uploadId) {
      const db = await dbPromise;
      const r = await tx<UploadSessionRecord | undefined>(db, "readonly", (s) => s.get(uploadId));
      return r;
    },
    async list() {
      const db = await dbPromise;
      const r = await tx<UploadSessionRecord[]>(db, "readonly", (s) => s.getAll());
      return r ?? [];
    },
    async delete(uploadId) {
      const db = await dbPromise;
      await tx(db, "readwrite", (s) => s.delete(uploadId));
    },
    async clear() {
      const db = await dbPromise;
      await tx(db, "readwrite", (s) => s.clear());
    },
  };
}

export function fileIdentityOf(file: File): FileIdentity {
  return {
    name: file.name,
    size: file.size,
    lastModified: file.lastModified,
    type: file.type,
  };
}
