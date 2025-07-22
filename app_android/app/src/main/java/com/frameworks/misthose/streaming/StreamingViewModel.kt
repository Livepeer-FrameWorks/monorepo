package com.frameworks.misthose.streaming

import android.app.Application
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.LiveData
import androidx.lifecycle.MutableLiveData
import androidx.lifecycle.viewModelScope
import com.frameworks.misthose.models.StreamProtocol
import com.frameworks.misthose.models.StreamingState
import com.frameworks.misthose.models.StreamingStats
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.text.SimpleDateFormat
import java.util.*

class StreamingViewModel(application: Application) : AndroidViewModel(application) {
    
    private val _streamingState = MutableLiveData<StreamingState>()
    val streamingState: LiveData<StreamingState> = _streamingState
    
    private val _streamingStats = MutableLiveData<StreamingStats>()
    val streamingStats: LiveData<StreamingStats> = _streamingStats
    
    private val _errorMessage = MutableLiveData<String>()
    val errorMessage: LiveData<String> = _errorMessage
    
    private var streamingStartTime: Long = 0
    private var isUsingFrontCamera = false
    private var currentClusterId: String? = null
    private var currentStreamId: String? = null
    
    private var streamingEngine: StreamingEngine? = null
    
    init {
        _streamingState.value = StreamingState()
        _streamingStats.value = StreamingStats()
    }
    
    fun startStreaming(url: String, protocol: StreamProtocol, clusterId: String? = null) {
        viewModelScope.launch {
            try {
                _streamingState.value = StreamingState(
                    isStreaming = false,
                    status = "Connecting...",
                    url = url,
                    protocol = protocol
                )
                
                currentClusterId = clusterId
                
                // Initialize streaming engine based on protocol
                streamingEngine = when (protocol) {
                    StreamProtocol.RTMP -> RTMPStreamingEngine()
                    StreamProtocol.SRT -> SRTStreamingEngine()
                    StreamProtocol.WHIP -> WHIPStreamingEngine()
                }
                
                // Start streaming
                val success = streamingEngine?.startStreaming(url) ?: false
                
                if (success) {
                    streamingStartTime = System.currentTimeMillis()
                    currentStreamId = generateStreamId()
                    
                    _streamingState.value = StreamingState(
                        isStreaming = true,
                        status = "Live",
                        url = url,
                        protocol = protocol
                    )
                    
                    // Notify FrameWorks API about stream start
                    notifyStreamStart(clusterId, protocol)
                    
                    startStatsUpdates()
                } else {
                    _streamingState.value = StreamingState(
                        isStreaming = false,
                        status = "Failed to connect",
                        url = url,
                        protocol = protocol
                    )
                    _errorMessage.value = "Failed to start streaming to $url"
                }
                
            } catch (e: Exception) {
                _streamingState.value = StreamingState(
                    isStreaming = false,
                    status = "Error: ${e.message}",
                    url = url,
                    protocol = protocol
                )
                _errorMessage.value = "Streaming error: ${e.message}"
            }
        }
    }
    
    fun stopStreaming() {
        viewModelScope.launch {
            _streamingState.value = _streamingState.value?.copy(
                isStreaming = false,
                status = "Disconnecting..."
            )
            
            // Notify FrameWorks API about stream stop
            notifyStreamStop()
            
            streamingEngine?.stopStreaming()
            streamingEngine = null
            
            currentClusterId = null
            currentStreamId = null
            
            _streamingState.value = StreamingState()
            _streamingStats.value = StreamingStats()
        }
    }
    
    fun switchCamera() {
        isUsingFrontCamera = !isUsingFrontCamera
    }
    
    fun isUsingFrontCamera(): Boolean = isUsingFrontCamera
    
    private fun notifyStreamStart(clusterId: String?, protocol: StreamProtocol) {
        // In a real implementation, this would call the FrameWorks API
        // to notify about stream start and get proper stream configuration
        viewModelScope.launch {
            try {
                // Mock API call for now
                println("Notifying FrameWorks API: Stream started on cluster $clusterId with protocol $protocol")
                
                // This would be:
                // val api = ApiClient.getApi()
                // val response = api.startStream(
                //     "Bearer $token",
                //     StartStreamRequest(
                //         clusterId = clusterId ?: "",
                //         protocol = protocol.name.lowercase(),
                //         settings = StreamSettings(
                //             resolution = getSelectedResolution(),
                //             bitrate = getSelectedBitrate(),
                //             fps = 30
                //         )
                //     )
                // )
                // currentStreamId = response.body()?.streamId
                
            } catch (e: Exception) {
                println("Failed to notify stream start: ${e.message}")
            }
        }
    }
    
    private fun notifyStreamStop() {
        viewModelScope.launch {
            try {
                if (currentClusterId != null && currentStreamId != null) {
                    // Mock API call for now
                    println("Notifying FrameWorks API: Stream stopped - cluster: $currentClusterId, stream: $currentStreamId")
                    
                    // This would be:
                    // val api = ApiClient.getApi()
                    // api.stopStream(
                    //     "Bearer $token",
                    //     StopStreamRequest(
                    //         clusterId = currentClusterId!!,
                    //         streamId = currentStreamId!!
                    //     )
                    // )
                }
            } catch (e: Exception) {
                println("Failed to notify stream stop: ${e.message}")
            }
        }
    }
    
    private fun startStatsUpdates() {
        viewModelScope.launch {
            while (_streamingState.value?.isStreaming == true) {
                val currentTime = System.currentTimeMillis()
                val duration = formatDuration(currentTime - streamingStartTime)
                
                // Get stats from streaming engine
                val engineStats = streamingEngine?.getStats()
                
                _streamingStats.value = StreamingStats(
                    bitrate = engineStats?.bitrate ?: generateMockBitrate(),
                    fps = engineStats?.fps ?: generateMockFPS(),
                    duration = duration,
                    bytesTransferred = engineStats?.bytesTransferred ?: 0,
                    droppedFrames = engineStats?.droppedFrames ?: 0
                )
                
                delay(1000) // Update every second
            }
        }
    }
    
    private fun formatDuration(durationMs: Long): String {
        val seconds = (durationMs / 1000) % 60
        val minutes = (durationMs / (1000 * 60)) % 60
        val hours = (durationMs / (1000 * 60 * 60)) % 24
        return String.format("%02d:%02d:%02d", hours, minutes, seconds)
    }
    
    private fun generateStreamId(): String {
        return "stream_${System.currentTimeMillis()}_${(1000..9999).random()}"
    }
    
    // Mock data generators for development
    private fun generateMockBitrate(): Int {
        return (2000..3000).random()
    }
    
    private fun generateMockFPS(): Int {
        return (28..30).random()
    }
    
    override fun onCleared() {
        super.onCleared()
        streamingEngine?.stopStreaming()
    }
} 