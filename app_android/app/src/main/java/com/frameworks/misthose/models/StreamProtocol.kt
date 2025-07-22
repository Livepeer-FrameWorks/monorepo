package com.frameworks.misthose.models

enum class StreamProtocol(val displayName: String, val defaultPort: Int) {
    SRT("SRT", 9999),
    WHIP("WHIP (WebRTC)", 443);
    
    companion object {
        fun fromString(value: String): StreamProtocol {
            return values().find { it.name.equals(value, ignoreCase = true) } ?: SRT
        }
    }
} 