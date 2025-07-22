package com.frameworks.misthose.api

import com.frameworks.misthose.models.*
import retrofit2.Response
import retrofit2.http.*

interface FrameWorksApi {
    
    @POST("auth/login")
    suspend fun login(@Body request: LoginRequest): Response<LoginResponse>
    
    @POST("auth/register")
    suspend fun register(@Body request: RegisterRequest): Response<LoginResponse>
    
    @GET("me")
    suspend fun getCurrentUser(@Header("Authorization") token: String): Response<User>
    
    @GET("clusters")
    suspend fun getClusters(@Header("Authorization") token: String): Response<List<Cluster>>
    
    @GET("clusters/{clusterId}/config")
    suspend fun getClusterConfig(
        @Header("Authorization") token: String,
        @Path("clusterId") clusterId: String
    ): Response<ClusterConfig>
    
    @POST("streams/start")
    suspend fun startStream(
        @Header("Authorization") token: String,
        @Body request: StartStreamRequest
    ): Response<StreamResponse>
    
    @POST("streams/stop")
    suspend fun stopStream(
        @Header("Authorization") token: String,
        @Body request: StopStreamRequest
    ): Response<Unit>

    // Analytics endpoints
    @GET("analytics/streams")
    suspend fun getStreamAnalytics(
        @Header("Authorization") token: String
    ): Response<List<StreamAnalytics>>

    @GET("analytics/streams/{streamId}")
    suspend fun getStreamDetails(
        @Header("Authorization") token: String,
        @Path("streamId") streamId: String
    ): Response<StreamAnalytics>

    @GET("analytics/streams/{streamId}/events")
    suspend fun getStreamEvents(
        @Header("Authorization") token: String,
        @Path("streamId") streamId: String
    ): Response<List<StreamEvent>>

    @GET("analytics/streams/{streamId}/viewers")
    suspend fun getStreamViewers(
        @Header("Authorization") token: String,
        @Path("streamId") streamId: String
    ): Response<ViewerStats>

    @GET("analytics/realtime/streams")
    suspend fun getRealtimeStreams(
        @Header("Authorization") token: String
    ): Response<RealtimeStreams>

    @GET("analytics/realtime/viewers")
    suspend fun getRealtimeViewers(
        @Header("Authorization") token: String
    ): Response<RealtimeViewers>

    @GET("analytics/node-metrics")
    suspend fun getNodeMetrics(
        @Header("Authorization") token: String
    ): Response<List<NodeMetrics>>

    @GET("analytics/stream-health")
    suspend fun getStreamHealth(
        @Header("Authorization") token: String,
        @Query("streamId") streamId: String
    ): Response<StreamHealth>
}

data class LoginRequest(
    val email: String,
    val password: String
)

data class RegisterRequest(
    val email: String,
    val password: String,
    val name: String
)

data class LoginResponse(
    val user: User,
    val token: String
)

data class ClusterConfig(
    val clusterId: String,
    val name: String,
    val region: String,
    val ingestEndpoints: List<IngestEndpoint>,
    val replicationTargets: List<ReplicationTarget>,
    val webhookUrl: String?
)

data class IngestEndpoint(
    val name: String,
    val url: String,
    val protocol: String,
    val isActive: Boolean = true
)

data class ReplicationTarget(
    val name: String,
    val url: String,
    val region: String,
    val platforms: List<String> // e.g., ["chaturbate", "stripchat"]
)

data class StartStreamRequest(
    val clusterId: String,
    val protocol: String,
    val settings: StreamSettings
)

data class StopStreamRequest(
    val clusterId: String,
    val streamId: String
)

data class StreamSettings(
    val resolution: String,
    val bitrate: Int,
    val fps: Int
)

data class StreamResponse(
    val streamId: String,
    val ingestUrl: String,
    val status: String
) 

// Analytics data classes
data class StreamAnalytics(
    val id: String,
    val tenantId: String,
    val streamId: String,
    val internalName: String,
    val sessionStartTime: String,
    val sessionEndTime: String?,
    val totalSessionDuration: Int,
    val currentViewers: Int,
    val peakViewers: Int,
    val totalConnections: Int,
    val bandwidthIn: Long,
    val bandwidthOut: Long,
    val totalBandwidthGB: Double,
    val bitrateKbps: Int,
    val resolution: String,
    val packetsSent: Long,
    val packetsLost: Long,
    val packetsRetrans: Long,
    val nodeId: String,
    val nodeName: String,
    val latitude: Double,
    val longitude: Double,
    val location: String,
    val status: String,
    val avgViewers: Double,
    val uniqueCountries: Int,
    val uniqueCities: Int,
    val avgBufferHealth: Float,
    val avgBitrate: Int,
    val packetLossRate: Float
)

data class StreamEvent(
    val timestamp: String,
    val eventType: String,
    val eventData: Map<String, Any>,
    val streamId: String,
    val nodeId: String
)

data class ViewerStats(
    val currentViewers: Int,
    val peakViewers: Int,
    val totalConnections: Int,
    val viewerHistory: List<ViewerHistoryPoint>,
    val geoStats: GeoStats
)

data class ViewerHistoryPoint(
    val timestamp: String,
    val viewerCount: Int,
    val connectionType: String,
    val bufferHealth: Float,
    val connectionQuality: Float,
    val countryCode: String,
    val city: String
)

data class GeoStats(
    val uniqueCountries: Int,
    val uniqueCities: Int,
    val countryBreakdown: Map<String, Int>,
    val cityBreakdown: Map<String, Map<String, Int>>
)

data class RealtimeStreams(
    val streams: List<RealtimeStream>,
    val count: Int
)

data class RealtimeStream(
    val streamId: String,
    val internalName: String,
    val currentViewers: Int,
    val bandwidthIn: Long,
    val bandwidthOut: Long,
    val status: String,
    val nodeId: String,
    val location: String,
    val viewerTrend: Double,
    val bufferHealth: Float,
    val connectionQuality: Float,
    val uniqueCountries: Int
)

data class RealtimeViewers(
    val totalViewers: Int,
    val streamViewers: List<StreamViewers>
)

data class StreamViewers(
    val streamId: String,
    val internalName: String,
    val avgViewers: Double,
    val peakViewers: Int,
    val uniqueCountries: Int,
    val uniqueCities: Int
)

data class NodeMetrics(
    val timestamp: String,
    val nodeId: String,
    val cpuUsage: Float,
    val memoryUsage: Float,
    val diskUsage: Float,
    val bandwidthIn: Long,
    val bandwidthOut: Long,
    val connectionsCurrent: Int,
    val healthScore: Float,
    val isHealthy: Boolean,
    val latitude: Double,
    val longitude: Double,
    val tags: List<String>,
    val metadata: Map<String, Any>
)

data class StreamHealth(
    val timestamp: String,
    val tenantId: String,
    val streamId: String,
    val internalName: String,
    val nodeId: String,
    val bitrate: Int,
    val fps: Float,
    val gopSize: Int,
    val width: Int,
    val height: Int,
    val bufferSize: Int,
    val bufferUsed: Int,
    val bufferHealth: Float,
    val packetsSent: Long,
    val packetsLost: Long,
    val packetsRetransmitted: Long,
    val bandwidthIn: Long,
    val bandwidthOut: Long,
    val codec: String,
    val profile: String,
    val trackMetadata: Map<String, Any>
) 