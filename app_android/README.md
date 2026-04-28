# Android App (Scaffold)

Status: experimental Android app scaffold. This module contains prototype camera, provider,
and streaming code, but it is not feature-complete and should not be used in production.

---

## Current State

- Project scaffolding plus prototype camera/provider screens
- Experimental SRT path using MediaCodec and the Haivision SRT library
- Incomplete WHIP/WebRTC path: peer connection and offer creation exist, but the WHIP HTTP
  exchange is still a placeholder
- Camera settings and some CameraX/Camera2 control plumbing exist, but the production camera
  pipeline is not wired end-to-end
- FrameWorks service integration is placeholder/mock code; current app code should not be
  treated as matching the production Gateway API

### Intended Usage

1. **Select a Provider**: Choose FrameWorks (default) or add a custom streaming target
2. **Configure Camera**: Adjust resolution, frame rate, and camera settings
3. **Start Streaming**: Tap the stream button to begin broadcasting
4. **Monitor**: Watch real-time statistics and connection status

## Planned Features

These are planned; timelines tracked in the roadmap:

- Production-grade multi-protocol ingest for SRT and WHIP/WebRTC
- Camera pipeline with reliable manual controls (focus, exposure, WB)
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
- **Implementation status**: Prototype MediaCodec encoder and SRT socket path

### WHIP (WebRTC-HTTP Ingestion Protocol)

- **Use Case**: Real-time streaming ingestion
- **Latency**: Sub-second
- **Reliability**: Modern web standard with automatic adaptation
- **Configuration**: WHIP endpoint URL
- **Implementation status**: WebRTC setup and offer creation exist; WHIP POST/answer handling is
  not implemented yet
