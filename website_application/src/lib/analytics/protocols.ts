export function protocolLabel(protocol: string): string {
  return protocol.toLowerCase() === "webvtt" ? "WebVTT" : protocol.toUpperCase();
}
