export type EngineState =
  | "idle"
  | "preparing"
  | "uploading"
  | "paused"
  | "completing"
  | "completed"
  | "failed"
  | "aborted";

export type PartStatus = "pending" | "in_flight" | "completed" | "failed";

export interface PartDescriptor {
  partNumber: number;
  presignedUrl: string;
}

export interface PartState {
  partNumber: number;
  presignedUrl: string;
  status: PartStatus;
  etag?: string;
  attempts: number;
  lastError?: string;
}

export interface CompletedPart {
  partNumber: number;
  etag: string;
}

export interface FileIdentity {
  name: string;
  size: number;
  lastModified: number;
  type: string;
}

export interface UploadSessionRecord {
  uploadId: string;
  file: FileIdentity;
  partSize: number;
  totalParts: number;
  parts: PartState[];
  createdAt: number;
  lastTouchedAt: number;
}

export type EngineEvent =
  | { type: "stateChange"; state: EngineState }
  | { type: "progress"; uploadedBytes: number; totalBytes: number; percent: number }
  | { type: "partCompleted"; partNumber: number; etag: string }
  | { type: "partFailed"; partNumber: number; error: string; attempt: number; willRetry: boolean }
  | { type: "transferComplete"; parts: CompletedPart[] }
  | { type: "error"; error: string };

export type EngineListener = (event: EngineEvent) => void;
