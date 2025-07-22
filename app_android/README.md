# <TBA>> - Professional Mobile Streaming App

<TBA>> is a comprehensive Android streaming application that supports multiple streaming protocols and provides professional-grade camera controls. Built for content creators, broadcasters, and streaming professionals who need reliable mobile streaming capabilities.

## ⚠️ HERE BE DRAGONS ⚠️

**Mostly stub functions - just a start to scope out what it would look like.**

---

## Features

### 🎥 **Multi-Protocol Streaming Support**
- **SRT**: Low-latency streaming with error correction for professional broadcasting
- **WHIP (WebRTC)**: Modern web-based streaming ingestion with ultra-low latency

### 📱 **Professional Camera Controls**
- **Manual Focus**: Precise focus control with distance adjustment
- **Exposure Control**: Manual exposure time, ISO, and compensation settings
- **White Balance**: Multiple presets (Auto, Daylight, Cloudy, Tungsten, etc.) and manual control
- **Scene Modes**: Optimized settings for Portrait, Landscape, Night, Sports, and more
- **Image Stabilization**: Both optical and digital stabilization support
- **Advanced Settings**: Noise reduction, edge enhancement, hot pixel correction
- **Zoom Control**: Smooth zoom with real-time adjustment
- **Flash/Torch**: Configurable flash modes and torch control

### 🎛️ **Flexible Input Sources**
- **Camera Input**: Front and rear cameras with seamless switching
- **Screen Capture**: Stream your device screen (coming soon)
- **Audio Sources**: Multiple audio input options (Microphone, Camcorder, etc.)
- **Audio Controls**: Gain adjustment and source selection

### 🔗 **Provider System**
- **FrameWorks Integration**: Native support for FrameWorks streaming service
- **Static Targets**: Direct streaming to RTMP/SRT/WHIP endpoints
- **Custom Services**: Support for any streaming service with configurable endpoints
- **Multiple Authentication**: JWT, OAuth 2.0, API Key, and Basic Auth support

### ⚙️ **Stream Configuration**
- **Resolution Options**: 480p, 720p, 1080p, 4K support
- **Frame Rates**: 15, 24, 30, 60 FPS options
- **Bitrate Control**: Adjustable bitrate from 500 kbps to 10+ Mbps
- **Quality Presets**: Optimized settings for different use cases

### 📊 **Real-time Monitoring**
- **Stream Statistics**: Bitrate, FPS, duration, dropped frames
- **Connection Status**: Real-time connection monitoring
- **Performance Metrics**: Network type, latency information

## Quick Start

### Prerequisites
- Android 7.0 (API level 24) or higher
- Camera and microphone permissions
- Network connectivity

### Installation
1. Clone the repository
2. Open in Android Studio
3. Build and install on your device
4. Grant camera and microphone permissions

### Basic Usage
1. **Select a Provider**: Choose FrameWorks (default) or add a custom streaming target
2. **Configure Camera**: Adjust resolution, frame rate, and camera settings
3. **Start Streaming**: Tap the stream button to begin broadcasting
4. **Monitor**: Watch real-time statistics and connection status

## Provider Configuration

### FrameWorks Service
1. Select "FrameWorks" as your provider
2. Log in with your FrameWorks credentials
3. The app will automatically configure streaming settings

### Static SRT Target
1. Tap "Add Provider" → "SRT Server"
2. Configure SRT settings:
   - **Name**: Display name for the provider
   - **Server URL**: SRT server address
   - **Port**: SRT port (default: 9999)
   - **Latency**: Buffer latency in milliseconds
   - **Encryption**: Optional passphrase for encrypted streams

### WHIP/WebRTC Target
1. Tap "Add Provider" → "WHIP Server"
2. Enter WHIP endpoint:
   - **Name**: Display name for the provider
   - **Server URL**: WHIP endpoint URL
   - **Bearer Token**: Optional authentication token

### Custom Service
1. Tap "Add Provider" → "Custom Service"
2. Configure service details:
   - **Base URL**: API base URL
   - **Authentication**: Choose auth method (JWT, OAuth, API Key, etc.)
   - **Endpoints**: Customize API endpoints if needed

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

## Technical Architecture

### Core Components
- **CameraController**: Camera management and manual controls
- **StreamingEngine**: Multi-protocol streaming implementation
- **ProviderManager**: Service and target management
- **AuthRepository**: Authentication handling

### Streaming Engines
- **SrtStreamingEngine**: Official SRT library with MediaCodec integration
- **WebRtcStreamingEngine**: Native WebRTC implementation

### Libraries Used
- **CameraX**: Modern camera API
- **Camera2**: Low-level camera controls
- **Official SRT Library**: Native SRT streaming with optimal performance
- **WebRTC**: Real-time communication
- **OkHttp/Retrofit**: Network communication

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