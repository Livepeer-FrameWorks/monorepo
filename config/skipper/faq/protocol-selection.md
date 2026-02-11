# Protocol Selection Guide for FrameWorks Ingest

## Protocol Comparison

| Factor               | RTMP                  | SRT                      | WHIP (WebRTC)                    |
| -------------------- | --------------------- | ------------------------ | -------------------------------- |
| Transport            | TCP                   | UDP                      | UDP (via HTTPS setup)            |
| FrameWorks port      | 1935/tcp              | 8889/udp                 | 443/tcp (HTTPS)                  |
| Glass-to-glass       | 2-5 seconds           | 0.5-2 seconds            | Under 500 ms                     |
| Packet loss handling | None (TCP retransmit) | ARQ retransmission       | NACK + FEC                       |
| Firewall             | Good (TCP)            | May be blocked (UDP)     | Excellent (HTTPS)                |
| Encryption           | RTMPS (TLS wrap)      | AES-128/256 built-in     | DTLS built-in                    |
| Encoder support      | Universal             | OBS, FFmpeg, vMix, Larix | Browser only (StreamCrafter SDK) |

## When to Use Each Protocol

### RTMP

Best for: general-purpose streaming with desktop encoders.

- Widest encoder compatibility (every streaming app supports RTMP)
- Simple setup: just paste the ingest URL and stream key
- Works behind most firewalls (TCP port 1935 is rarely blocked)
- Adequate latency for most use cases (2-5 seconds)
- Use RTMPS for encrypted transport

FrameWorks URL: `rtmp://edge-ingest.frameworks.network/live/{streamKey}`

### SRT (Secure Reliable Transport)

Best for: unreliable networks, professional contribution, long-distance feeds.

- Handles packet loss through automatic retransmission (ARQ)
- Tunable latency via the latency parameter (trade latency for loss resilience)
- Built-in AES encryption without TLS overhead
- Better than RTMP on cellular, satellite, or congested networks

FrameWorks URL: `srt://edge-ingest.frameworks.network:8889?streamid={streamKey}`

### WHIP (WebRTC-HTTP Ingestion Protocol)

Best for: browser-based ingest, lowest-latency scenarios.

- Sub-500ms glass-to-glass latency
- No software installation needed (runs in browser)
- Uses the StreamCrafter SDK for camera, screen share, and multi-source
- WebRTC encoding means no B-frame issues
- Requires HTTPS (works through all firewalls)

FrameWorks URL: `https://edge-ingest.frameworks.network/webrtc/{streamKey}`

## SRT Parameter Tuning

Source: https://github.com/Haivision/srt/blob/master/docs/API/API-socket-options.md

### Key Defaults

| Parameter              | Default     | Description                     |
| ---------------------- | ----------- | ------------------------------- |
| Latency (SRTO_LATENCY) | 120 ms      | Buffer size for loss recovery   |
| Bandwidth overhead     | 25%         | Extra bandwidth for retransmits |
| Max segment size (MSS) | 1,500 bytes | UDP payload size                |
| Connection timeout     | 3,000 ms    | Time to establish connection    |
| Peer idle timeout      | 5,000 ms    | Disconnect after inactivity     |
| Flow control window    | 25,600 pkts | Max unacknowledged packets      |

### Latency Tuning

Source: https://doc.haivision.com/SRT/1.5.4/Haivision/latency

- Recommended: 4x the round-trip time (RTT) on networks with 0.1-0.2% packet loss
- Minimum viable: 3x RTT
- Valid range: 20 ms to 8,000 ms
- The higher value between sender and receiver is always used (negotiated)
- Higher latency = more time to recover lost packets = fewer visible artifacts
- Lower latency = faster delivery = less resilience to network issues

For a typical cross-country path with 50ms RTT and moderate loss, 200-300ms latency is a good starting point. For intercontinental paths (150ms+ RTT), 500-1000ms may be needed.

## Protocol Selection Decision Tree

1. Is the source a web browser? Use **WHIP** (StreamCrafter SDK).
2. Is sub-second latency required? Use **WHIP** for browser or consider **SRT** with aggressive latency settings for desktop.
3. Is the network unreliable (cellular, satellite, long-distance, high packet loss)? Use **SRT** with latency set to 4x RTT.
4. Is UDP port 8889 blocked by the network? Use **RTMP** (TCP port 1935) or **WHIP** (HTTPS port 443).
5. Default: use **RTMP** for simplest setup and widest compatibility.
