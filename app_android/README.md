# Android App (Scaffold)

Status: Scaffold only. This module currently contains initial structure and placeholder code. It is not feature‑complete and should not be used in production.

---

## Current State

- Project scaffolding and placeholder code only
- No supported streaming protocols yet
- No camera pipeline or controls yet

## Quick Start

### Prerequisites
- Android 7.0 (API level 24) or higher
- Camera and microphone permissions
- Network connectivity

### Installation
1. Clone the repository
2. Open `app_android` in Android Studio
3. Build succeeds with stubs; the app is not functional

### Basic Usage
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

## Notes for Contributors

- Keep placeholders minimal and clearly labeled
- Avoid shipping UI that implies functionality that isn’t implemented
- Update this README when a feature becomes functional

## Requirements

### Device Requirements
- **OS**: Android 7.0+ (API 24)
- **RAM**: 3GB+ recommended
- **Storage**: 100MB+ free space
- **Camera**: Rear camera required, front camera optional
- **Network**: Wi-Fi or cellular data connection

### Permissions
- **Camera**: Video capture
- **Microphone**: Audio capture
- **Network**: Internet connectivity
- **Storage**: Settings and cache (Android < 10)
- **Foreground Service**: Background streaming

## Troubleshooting

### Common Issues

**Camera not working**
- Check camera permissions
- Ensure camera is not in use by another app
- Try switching between front/rear cameras

**Streaming connection fails**
- Verify network connectivity
- Check provider configuration
- Ensure firewall allows streaming ports

**Poor stream quality**
- Reduce resolution or frame rate
- Lower bitrate for weak connections
- Check device temperature (thermal throttling)

**Audio issues**
- Check microphone permissions
- Verify audio source selection
- Test with different audio gain settings

### Performance Optimization

**For better streaming performance:**
- Close unnecessary background apps
- Use Wi-Fi instead of cellular when possible
- Keep device plugged in during long streams
- Monitor device temperature
- Use lower settings on older devices

**For better video quality:**
- Ensure good lighting conditions
- Use manual focus for critical shots
- Adjust white balance for lighting conditions
- Enable stabilization for handheld streaming

## Contributing

We welcome contributions! Please see our contributing guidelines for:
- Code style requirements
- Testing procedures
- Pull request process
- Issue reporting

## License

This project is licensed under the MIT License - see the LICENSE file for details.

## Support

- **Documentation**: Check this README and in-app help
- **Issues**: Report bugs via GitHub Issues
- **Community**: Join our Discord server for real-time support
- **Professional Support**: Contact us for enterprise solutions

---

**Built for professional streamers, by professional developers.** 
