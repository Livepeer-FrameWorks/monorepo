/**
 * RawChunkParser - Binary Frame Header Parser
 *
 * Parses the 12-byte binary header from MistServer's raw WebSocket stream.
 *
 * Header format:
 *   Byte 0:     Track index (uint8)
 *   Byte 1:     Chunk type: 0=delta, 1=key, 2=init
 *   Bytes 2-9:  Timestamp in milliseconds (uint64 big-endian)
 *   Bytes 10-11: Offset in milliseconds (int16 big-endian, signed)
 *
 * The offset is server-calculated and used for A/V synchronization.
 * Combined presentation time = timestamp + offset
 */

import type { RawChunk, ChunkType } from "./types";

const HEADER_LENGTH = 12;

/**
 * Parse chunk type byte to string
 */
function parseChunkType(typeByte: number): ChunkType {
  switch (typeByte) {
    case 1:
      return "key";
    case 2:
      return "init";
    default:
      return "delta";
  }
}

/**
 * Parse a raw binary chunk from MistServer
 *
 * @param data - ArrayBuffer containing header + frame data
 * @returns Parsed RawChunk object
 * @throws Error if data is too short
 */
export function parseRawChunk(data: ArrayBuffer): RawChunk {
  if (data.byteLength < HEADER_LENGTH) {
    throw new Error(
      `Invalid chunk: expected at least ${HEADER_LENGTH} bytes, got ${data.byteLength}`
    );
  }

  const headerView = new DataView(data, 0, HEADER_LENGTH);

  // Byte 0: track index
  const trackIndex = headerView.getUint8(0);

  // Byte 1: chunk type
  const type = parseChunkType(headerView.getUint8(1));

  // Bytes 2-9: timestamp (uint64 big-endian)
  // getBigUint64 returns BigInt, convert to Number (safe for values < 2^53)
  const timestampBig = headerView.getBigUint64(2, false); // false = big-endian
  const timestamp = Number(timestampBig);

  // Bytes 10-11: offset (int16 big-endian, signed)
  const offset = headerView.getInt16(10, false); // false = big-endian

  // Remaining bytes: actual frame data
  // Use slice() to create a copy (like reference rawws.js line 462)
  // This ensures the data survives buffer transfers
  const frameData = new Uint8Array(data.slice(HEADER_LENGTH));

  return {
    trackIndex,
    type,
    timestamp,
    offset,
    data: frameData,
  };
}

/**
 * Calculate the presentation timestamp for a chunk
 * This combines the server timestamp with the sync offset
 *
 * @param chunk - Parsed raw chunk
 * @returns Presentation timestamp in microseconds (for WebCodecs API)
 */
export function getPresentationTimestamp(chunk: RawChunk): number {
  // timestamp and offset are in milliseconds
  // WebCodecs expects microseconds
  return (chunk.timestamp + chunk.offset) * 1000;
}

/**
 * Check if this chunk is a keyframe
 */
export function isKeyframe(chunk: RawChunk): boolean {
  return chunk.type === "key";
}

/**
 * Check if this chunk contains codec initialization data
 */
export function isInitData(chunk: RawChunk): boolean {
  return chunk.type === "init";
}

/**
 * Format chunk for debug logging
 */
export function formatChunkForLog(chunk: RawChunk): string {
  const pts = (chunk.timestamp + chunk.offset) / 1000; // seconds
  const ptsFormatted = pts.toFixed(3);
  return `[Track ${chunk.trackIndex}] ${chunk.type.toUpperCase()} @ ${ptsFormatted}s (${chunk.data.byteLength} bytes)`;
}

/**
 * RawChunkParser class for stateful parsing with validation
 */
export class RawChunkParser {
  private debug: boolean;

  constructor(options: { debug?: boolean } = {}) {
    this.debug = options.debug ?? false;
  }

  /**
   * Parse binary data from WebSocket
   *
   * @param data - ArrayBuffer from WebSocket message
   * @returns Parsed chunk or null if invalid
   */
  parse(data: ArrayBuffer): RawChunk | null {
    try {
      const chunk = parseRawChunk(data);

      if (this.debug) {
        console.log("▶️", formatChunkForLog(chunk));
      }

      return chunk;
    } catch (err) {
      console.error("▶️ Failed to parse chunk:", err);
      return null;
    }
  }

  /**
   * Set debug mode
   */
  setDebug(enabled: boolean | "verbose"): void {
    this.debug = enabled === "verbose" || enabled === true;
  }
}
