# QoE Prediction & Automated Remediation (2025-2026)

Research synthesized from current sources. Sources linked throughout.

## QoE Prediction: State of the Art

### ML-Based Prediction (Production Standard)

- **Random Forest models** achieve 95.8% accuracy for user satisfaction prediction using
  network parameters (latency, packet loss, bandwidth) mapped to QoE metrics.
  Based on 20,000+ data records.
- **Deep learning** (LSTM, CNN, GNN) used for temporal QoE prediction — understanding
  how quality degrades over time, not just instantaneously.
- **Personalized QoE**: December 2025 paper ([arxiv.org/abs/2512.12736](https://arxiv.org/abs/2512.12736))
  proposes demographic-aware ML frameworks modeling varying user sensitivities to specific
  impairments (rebuffering, bitrate variation, quality degradation).

### Perceptual Quality Metrics

- **IMAX ViewerScore**: 94% correlation with human perception (up from 90% pre-AI).
  Cross-reference metric that validates encoding quality.
- **Synamedia pVMAF**: Real-time per-stream quality assessment without reference video.
  Enables live quality monitoring during broadcast.

### LLM-Augmented Analysis (Emerging)

- June 2025 paper ([arxiv.org/abs/2506.00924](https://arxiv.org/abs/2506.00924)): Bridges
  subjective and objective QoE by using LLM-based comment analysis compared against
  network MOS scores.

## Automated Remediation: What's Working

### Mid-Stream CDN Switching (Production)

The most mature automated remediation in streaming:

- **NPAW Active Switching**: Identifies better-performing CDNs and switches automatically
  during playback based on real-time viewer metrics.
- **Mlytics**: Flips traffic between CDNs in milliseconds using RUM-fed AI.
- **Quortex Switch** (March 2025): API-based multi-CDN switching with real-time dashboards.
- **Hydrolix CDN Insights** (November 2025): Real-time observability across multi-CDN
  environments for "instant remediation."

### Real-Time Encoding Adaptation (Production)

- **IMAX StreamSmart On-Air**: Dynamically adjusts encoder settings during live broadcasts
  to reduce bandwidth while maintaining perceived quality.
- **Synamedia AI quality agent**: Continuously monitors and proposes optimized ABR ladders.
  Replaces days of manual encoder testing with minutes of automated measurement.

### What Does NOT Exist at Production Scale

**Fully autonomous end-to-end remediation** where an AI system detects a quality problem,
diagnoses the root cause (encoder? CDN? last-mile?), and takes corrective action across
the entire pipeline without human involvement.

Current systems automate **specific remediation actions** (switch CDN, adjust bitrate)
but require **humans for cross-system diagnosis**.

## Proactive Monitoring: Layered Approach

The industry uses three layers, with ML as the production standard:

### 1. Rule-Based (Still Foundational)

- Threshold alerts: rebuffering ratio > X%, startup time > Y seconds
- Audio silence detection, logo presence checks
- Deterministic, explainable, fast

### 2. ML-Based (Current Production Standard)

- Anomaly detection without manual thresholds (NPAW NaLa, Conviva AI Alerts)
- Learns normal patterns, detects deviations automatically
- Eliminates "threshold fatigue"

### 3. LLM-Based (Emerging Interface Layer)

- NPAW NaLa conversational interface, Conviva Nexa NL dashboard queries
- NOC chatbots for incident triage being piloted
  ([BigPanda](https://www.bigpanda.io/blog/three-large-language-models-walk-into-a-network-operations-center/))
- Meta uses [LLMs for incident response](https://www.tryparity.com/blog/how-meta-uses-llms-to-improve-incident-response)
  summaries

**Key insight**: LLMs are used as the **interface layer** (natural language queries,
incident summarization) rather than as the detection engine itself. Detection remains
ML/statistical.

## Key Takeaway for Skipper

Skipper's heartbeat should use ML/statistical methods for anomaly detection (or leverage
existing Prometheus/Grafana alerts + future Lookout incidents). The LLM adds value in the
**investigation and explanation step** — chaining diagnostic tools, reasoning about
correlations, and producing natural-language root cause analysis.
