import { VOD_UPLOAD_MAX_FILENAME_BYTES } from "./formats";

export function validUploadFilenameLength(filename: string): boolean {
  return new TextEncoder().encode(filename).length <= VOD_UPLOAD_MAX_FILENAME_BYTES;
}
