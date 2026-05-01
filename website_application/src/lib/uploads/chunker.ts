export function sliceFilePart(file: Blob, partNumber: number, partSize: number): Blob {
  const start = (partNumber - 1) * partSize;
  const end = Math.min(start + partSize, file.size);
  return file.slice(start, end);
}

export function partByteRange(
  partNumber: number,
  partSize: number,
  totalSize: number
): { start: number; end: number; size: number } {
  const start = (partNumber - 1) * partSize;
  const end = Math.min(start + partSize, totalSize);
  return { start, end, size: end - start };
}
