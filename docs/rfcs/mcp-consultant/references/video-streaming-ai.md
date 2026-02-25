# AI in Video Streaming Operations (2025-2026)

Research synthesized from current sources. Sources linked throughout.

## Direct Competitors: Conversational AI for Streaming

### NPAW NaLa (Most Advanced)

[NPAW NaLa](https://npaw.com/solutions/nala/) is the closest product to Skipper's vision.

- **NaLa Sentinel**: Autonomous anomaly detection — tracks trends in real time, creates
  alerts without manual thresholds. Users adjust sensitivity.
- **NaLa Knowledge Base**: Customers upload business-context documents so NaLa's responses
  are customized to their specific infrastructure.
- **Natural language interface**: Users ask questions about video performance trends.
- At [NAB 2025](https://tvnewscheck.com/tech/article/nab-show-npaw-to-launch-next-gen-nala-ai-for-issue-resolution-analysis-automation-and-a-new-video-intelligence-suite/),
  announced next-gen version expanding to "issue resolution, analysis & automation."
- Also offers [Multi-CDN Active Switching](https://npaw.com/solutions/multi-cdn-active-switching/)
  — ML-driven CDN traffic steering based on real-time viewer metrics.

### Conviva (Nexa + AI Alerts)

[Conviva](https://www.conviva.ai/) — Named Gartner Visionary for Digital Experience Monitoring 2025.

- **AI Alerts**: Proactive anomaly detection comparing QoE metrics against recent norms.
  Detects anomalies and notifies publishers of "the issue, root cause, and likely solution."
  ([docs](https://docs.conviva.com/learning-center-files/content/eco/ai_alerts.html))
- **Nexa**: Natural-language interface for querying streaming analytics without specialist
  training. Teams can query behavioral patterns and outcome drivers through text.
- **Scale**: 2.5B+ unique sensors, billions of video streams analyzed.
- [HBO uses Conviva's AI to combat buffering](https://www.streamingmedia.com/Articles/News/Online-Video-News/HBO-Uses-AI-to-Combat-Buffering-with-Convivas-Help-120449.aspx)

### Touchstream VirtualNOC

[Touchstream](https://touchstream.media/) — AI-enhanced NOC for OTT operators.

- Monitors 15,000+ live streams continuously
- AI/ML automation layer between data collection and visualization
- Automates diagnostic data capture from synthetic monitoring probes
- Available on [AWS Marketplace](https://aws.amazon.com/marketplace/pp/prodview-xtvx4gbytt6ha)

## CDN/Platform Vendors (Not Offering AI Diagnostics)

### Mux

[Mux Data](https://www.mux.com/data) provides deep QoE observability but no AI assistant.
Metric dashboards + alerting, not conversational. Added Datadog integration and BigQuery/S3
streaming export in 2025. Per-title encoding uses AI for bitrate ladder optimization.

### Cloudflare

2025 focus: abuse detection at scale + [MoQ (Media over QUIC)](https://developers.cloudflare.com/moq/)
launch. First global MoQ relay network across 330+ cities. Swedish broadcaster SVT achieved
150ms e2e latency. No AI diagnostics for stream customers.

### AWS MediaLive

Build-it-yourself approach: [demo app](https://github.com/aws-samples/automating-livestream-video-monitoring)
using Rekognition for real-time monitoring (audio silence, logo verification, thumbnail analysis).
ML-based, not LLM-based.

### Akamai

Launched [Inference Cloud](https://www.akamai.com/products/akamai-inference-cloud-platform) with
NVIDIA (Oct 2025) — edge inference infrastructure, not streaming diagnostics. Positioning as
infra layer for others to build AI-powered streaming tools on.

### Fastly

Focus on edge observability (Edge Observer, Domain Inspector, OpenTelemetry). No AI-specific
streaming diagnostics.

## Encoding & Quality AI (Adjacent)

- **Bitmovin**: [AI-enabled encoding](https://bitmovin.com/bitmovin-launches-ai-enabled-encoding/)
  with per-title and three-pass optimization
- **IMAX StreamSmart**: ML-driven lower-bitrate encoding with ViewerScore (94% correlation
  with human perception). [On-Air](https://www.imax.com/sct/product/streamsmart-on-air)
  dynamically adjusts during live broadcasts.
- **Synamedia**: AI video quality agent automates encoder evaluation. pVMAF provides
  real-time per-stream quality assessment. ([IBC 2025 preview](https://www.sportsvideo.org/2025/09/08/ibc-2025-synamedia-redefines-ais-impact-on-user-experience/))

## Key Takeaway

NPAW NaLa and Conviva Nexa are the direct competitors. Neither offers cross-pipeline
diagnosis (encoder→CDN→player) in a single conversation. The market gap is an AI that
can trace a viewer symptom through the entire streaming pipeline to find root cause.
