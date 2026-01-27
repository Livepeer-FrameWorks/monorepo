# Android App (Scaffold)

Status: Scaffold only. This module currently contains initial structure and placeholder code. It is not feature‑complete and should not be used in production.

---

## Current State

- Project scaffolding and placeholder code only
- No supported streaming protocols yet
- No camera pipeline or controls yet

### Intended Usage

1. **Select a Provider**: Choose FrameWorks (default) or add a custom streaming target
2. **Configure Camera**: Adjust resolution, frame rate, and camera settings
3. **Start Streaming**: Tap the stream button to begin broadcasting
4. **Monitor**: Watch real-time statistics and connection status

## Planned Features

These are planned; timelines tracked in the roadmap:

- Multi‑protocol ingest: SRT and WHIP/WebRTC
- Camera pipeline with manual controls (focus, exposure, WB)
- Provider system: FrameWorks, static targets, custom endpoints
- Stream configuration presets (resolution, fps, bitrate)
- Basic stats overlay (bitrate, fps, dropped frames)

## Camera Settings

### Resolution & Quality

- **Resolution**: Choose from 480p to 4K based on device capabilities
- **Frame Rate**: Select optimal frame rate for your content
- **Bitrate**: Balance quality vs. bandwidth usage

### Manual Controls

- **Focus Mode**: Switch between auto, manual, and continuous focus
- **Focus Distance**: Manual focus control (0.0 = near, 1.0 = far)
- **Exposure Mode**: Auto, manual, shutter priority, or ISO priority
- **Exposure Compensation**: Adjust exposure from -2 to +2 EV
- **ISO**: Manual ISO control for low-light situations

### White Balance

- **Auto**: Automatic white balance adjustment
- **Presets**: Daylight, Cloudy, Shade, Tungsten, Fluorescent
- **Manual**: Custom white balance control

### Scene Modes

Optimized settings for different scenarios:

- **Portrait**: Enhanced skin tones and background blur
- **Landscape**: Sharp details and vibrant colors
- **Night**: Low-light optimization
- **Sports**: Fast motion capture
- **And more**: Sunset, Fireworks, Snow, Beach, etc.

## Streaming Protocols

### SRT (Secure Reliable Transport)

- **Use Case**: Professional broadcasting
- **Latency**: 1-5 seconds (configurable)
- **Reliability**: Error correction, encryption, and adaptive bitrate
- **Configuration**: Server, port, latency, encryption settings
- **Implementation**: Official SRT library with native MediaCodec integration

### WHIP (WebRTC-HTTP Ingestion Protocol)

- **Use Case**: Real-time streaming ingestion
- **Latency**: Sub-second
- **Reliability**: Modern web standard with automatic adaptation
- **Configuration**: WHIP endpoint URL
