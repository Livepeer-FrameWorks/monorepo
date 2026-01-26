package com.frameworks.misthose.models

import com.google.gson.annotations.SerializedName

data class StreamProvider(
    val id: String,
    val name: String,
    val type: ProviderType,
    val isDefault: Boolean = false,
    
    // For static providers
    val staticConfig: StaticProviderConfig? = null,
    
    // For service providers (FrameWorks-like)
    val serviceConfig: ServiceProviderConfig? = null
)

enum class ProviderType(val displayName: String) {
    STATIC("Static Target"),
    FRAMEWORKS("FrameWorks Service"),
    CUSTOM_SERVICE("Custom Service")
}

data class StaticProviderConfig(
    val protocol: StreamProtocol,
    val serverUrl: String,
    val port: Int,
    val streamKey: String,
    val username: String? = null,
    val password: String? = null,
    val useTLS: Boolean = false,
    
    // Additional protocol-specific settings
    val srtSettings: SrtSettings? = null,
    val whipSettings: WhipSettings? = null
)

data class ServiceProviderConfig(
    val baseUrl: String,
    val authType: AuthType,
    
    // Endpoints for different operations
    val endpoints: ServiceEndpoints,
    
    // Authentication details
    val authConfig: AuthConfig? = null
)

enum class AuthType(val displayName: String) {
    JWT("JWT Token"),
    OAUTH("OAuth 2.0"),
    BASIC("Basic Auth"),
    API_KEY("API Key"),
    NONE("No Authentication")
}

data class ServiceEndpoints(
    val login: String = "/auth/login",
    val refresh: String = "/auth/refresh",
    val streams: String = "/api/streams",
    val streamKey: String = "/api/streams/{id}/key",
    val startStream: String = "/api/streams/{id}/start",
    val stopStream: String = "/api/streams/{id}/stop",
    val streamStatus: String = "/api/streams/{id}/status"
)

data class AuthConfig(
    val username: String? = null,
    val password: String? = null,
    val apiKey: String? = null,
    val clientId: String? = null,
    val clientSecret: String? = null,
    val scope: String? = null,
    val token: String? = null,
    val refreshToken: String? = null,
    val tokenExpiry: Long? = null
)

// Protocol-specific settings
data class SrtSettings(
    val latency: Int = 200, // milliseconds
    val maxBandwidth: Long = -1, // -1 for auto
    val connectionTimeout: Int = 3000,
    val peerIdleTimeout: Int = 5000,
    val enableEncryption: Boolean = false,
    val passphrase: String? = null,
    val keyLength: Int = 16,
    val mode: SrtMode = SrtMode.CALLER
)

enum class SrtMode(val displayName: String) {
    CALLER("Caller"),
    LISTENER("Listener"),
    RENDEZVOUS("Rendezvous")
}

data class WhipSettings(
    val iceServers: List<IceServer> = emptyList(),
    val enableAudio: Boolean = true,
    val enableVideo: Boolean = true,
    val audioCodec: String = "opus",
    val videoCodec: String = "VP8",
    val bearerToken: String? = null,
    val timeout: Int = 30000
)

data class IceServer(
    val urls: List<String>,
    val username: String? = null,
    val credential: String? = null
)

// Built-in providers
object DefaultProviders {
    val frameworks = StreamProvider(
        id = "frameworks_default",
        name = "FrameWorks",
        type = ProviderType.FRAMEWORKS,
        isDefault = true,
        serviceConfig = ServiceProviderConfig(
            baseUrl = "https://api.frameworks.network",
            authType = AuthType.JWT,
            endpoints = ServiceEndpoints()
        )
    )
    
    fun createStaticSrtProvider(name: String, url: String, port: Int = 9999): StreamProvider {
        return StreamProvider(
            id = "static_srt_${System.currentTimeMillis()}",
            name = name,
            type = ProviderType.STATIC,
            staticConfig = StaticProviderConfig(
                protocol = StreamProtocol.SRT,
                serverUrl = url,
                port = port,
                streamKey = "",
                srtSettings = SrtSettings()
            )
        )
    }
    
    fun createStaticWhipProvider(name: String, url: String, bearerToken: String? = null): StreamProvider {
        return StreamProvider(
            id = "static_whip_${System.currentTimeMillis()}",
            name = name,
            type = ProviderType.STATIC,
            staticConfig = StaticProviderConfig(
                protocol = StreamProtocol.WHIP,
                serverUrl = url,
                port = 443,
                streamKey = "",
                whipSettings = WhipSettings(bearerToken = bearerToken)
            )
        )
    }
} 