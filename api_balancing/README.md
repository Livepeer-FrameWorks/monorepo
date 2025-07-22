# 🌊 Foghorn - Load Balancer

Go implementation of MistServer's load balancer, replacing the original C++ MistUtilLoad binary.

## Overview

Routes streaming traffic to the best available media nodes based on:
- Geographic proximity
- Node performance (CPU, RAM, bandwidth)
- Stream availability
- Configurable weights

## Integration

- Receives node health updates from Helmsman
- Provides 100% compatible API for MistServer nodes
- Posts routing decisions to analytics pipeline

See `env.example` for configuration options. 