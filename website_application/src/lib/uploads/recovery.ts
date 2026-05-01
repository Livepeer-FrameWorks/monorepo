import { fileIdentityOf, fileMatches, isExpired, type SessionStore } from "./session-store";
import type { CompletedPart, UploadSessionRecord } from "./types";

export interface RecoveryCandidate {
  record: UploadSessionRecord;
  completedParts: CompletedPart[];
}

export async function findRecoverable(
  store: SessionStore,
  file: File,
  now: number = Date.now()
): Promise<RecoveryCandidate | null> {
  const ident = fileIdentityOf(file);
  const all = await store.list();
  const fresh = all.filter((r) => !isExpired(r, now));
  // Drop expired sessions from the store opportunistically.
  for (const r of all) {
    if (isExpired(r, now)) {
      try {
        await store.delete(r.uploadId);
      } catch {
        // ignore
      }
    }
  }
  const match = fresh
    .filter((r) => fileMatches(r.file, ident))
    .sort((a, b) => b.lastTouchedAt - a.lastTouchedAt)[0];
  if (!match) return null;
  const completed: CompletedPart[] = match.parts
    .filter((p) => p.status === "completed" && p.etag)
    .map((p) => ({ partNumber: p.partNumber, etag: p.etag! }));
  return { record: match, completedParts: completed };
}

export async function dropSession(store: SessionStore, uploadId: string): Promise<void> {
  try {
    await store.delete(uploadId);
  } catch {
    // ignore
  }
}
