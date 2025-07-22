package com.frameworks.misthose.streaming

import android.content.Context
import android.util.Log
import android.view.Surface
import com.frameworks.misthose.models.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import org.webrtc.*
import java.io.File
import java.util.concurrent.Executors
import io.github.haivision.srt.SrtSocket
import io.github.haivision.srt.SrtSocketOptions
import android.media.MediaCodec
import android.media.MediaCodecInfo
import android.media.MediaFormat
import android.media.MediaMuxer
import java.nio.ByteBuffer

class StreamingEngine(private val context: Context) {
    
    private val TAG = "StreamingEngine"
    
    // Current streaming state
    private val _streamingState = MutableStateFlow(StreamingState.IDLE)
    val streamingState: StateFlow<StreamingState> = _streamingState.asStateFlow()
    
    private val _currentProvider = MutableStateFlow<StreamProvider?>(null)
    val currentProvider: StateFlow<StreamProvider?> = _currentProvider.asStateFlow()
    
    private val _streamingStats = MutableStateFlow(StreamingStats())
    val streamingStats: StateFlow<StreamingStats> = _streamingStats.asStateFlow()
    
    // Protocol-specific engines
    private var srtEngine: SrtStreamingEngine? = null
    private var webrtcEngine: WebRtcStreamingEngine? = null
    
    // Current streaming session
    private var currentEngine: StreamingEngineInterface? = null
    private var streamingJob: Job? = null
    
    // Input surface for camera
    private var inputSurface: Surface? = null
    
    fun startStreaming(provider: StreamProvider, cameraSettings: CameraSettings): Boolean {
        if (_streamingState.value != StreamingState.IDLE) {
            Log.w(TAG, "Already streaming or connecting")
            return false
        }
        
        _currentProvider.value = provider
        _streamingState.value = StreamingState.CONNECTING
        
        try {
            currentEngine = when (provider.type) {
                ProviderType.STATIC -> {
                    val config = provider.staticConfig ?: return false
                    createEngineForProtocol(config.protocol, provider, cameraSettings)
                }
                ProviderType.FRAMEWORKS, ProviderType.CUSTOM_SERVICE -> {
                    // For service providers, we'll need to get the streaming URL first
                    // For now, default to SRT for better performance and reliability
                    createEngineForProtocol(StreamProtocol.SRT, provider, cameraSettings)
                }
            }
            
            currentEngine?.let { engine ->
                streamingJob = CoroutineScope(Dispatchers.IO).launch {
                    try {
                        engine.initialize()
                        inputSurface = engine.getInputSurface()
                        
                        if (engine.startStreaming()) {
                            _streamingState.value = StreamingState.STREAMING
                            startStatsMonitoring()
                        } else {
                            _streamingState.value = StreamingState.ERROR
                        }
                    } catch (e: Exception) {
                        Log.e(TAG, "Error starting stream", e)
                        _streamingState.value = StreamingState.ERROR
                    }
                }
                return true
            }
            
        } catch (e: Exception) {
            Log.e(TAG, "Error initializing streaming engine", e)
            _streamingState.value = StreamingState.ERROR
        }
        
        return false
    }
    
    private fun createEngineForProtocol(
        protocol: StreamProtocol,
        provider: StreamProvider,
        cameraSettings: CameraSettings
    ): StreamingEngineInterface? {
        return when (protocol) {
            StreamProtocol.RTMP, StreamProtocol.RTMPS -> {
                Log.w(TAG, "RTMP protocol is not supported in this version. Use SRT or WebRTC instead.")
                null
            }
            StreamProtocol.SRT -> {
                srtEngine = SrtStreamingEngine(context, provider, cameraSettings)
                srtEngine
            }
            StreamProtocol.WHIP -> {
                webrtcEngine = WebRtcStreamingEngine(context, provider, cameraSettings)
                webrtcEngine
            }
        }
    }
    
    fun stopStreaming() {
        streamingJob?.cancel()
        currentEngine?.stopStreaming()
        currentEngine?.release()
        currentEngine = null
        inputSurface = null
        
        _streamingState.value = StreamingState.IDLE
        _currentProvider.value = null
        _streamingStats.value = StreamingStats()
    }
    
    fun getInputSurface(): Surface? = inputSurface
    
    fun updateSettings(cameraSettings: CameraSettings) {
        currentEngine?.updateSettings(cameraSettings)
    }
    
    private fun startStatsMonitoring() {
        CoroutineScope(Dispatchers.IO).launch {
            while (_streamingState.value == StreamingState.STREAMING) {
                currentEngine?.getStats()?.let { stats ->
                    _streamingStats.value = stats
                }
                delay(1000) // Update stats every second
            }
        }
    }
    
    fun release() {
        stopStreaming()
        srtEngine?.release()
        webrtcEngine?.release()
    }
}

// Interface for different streaming engines
interface StreamingEngineInterface {
    suspend fun initialize()
    fun getInputSurface(): Surface?
    fun startStreaming(): Boolean
    fun stopStreaming()
    fun updateSettings(cameraSettings: CameraSettings)
    fun getStats(): StreamingStats
    fun release()
}

// SRT Streaming Engine using official SRT library
class SrtStreamingEngine(
    private val context: Context,
    private val provider: StreamProvider,
    private val cameraSettings: CameraSettings
) : StreamingEngineInterface {
    
    private val TAG = "SrtStreamingEngine"
    private var srtSocket: SrtSocket? = null
    private var mediaCodec: MediaCodec? = null
    private var mediaMuxer: MediaMuxer? = null
    private var isStreaming = false
    private var startTime = 0L
    private var inputSurface: Surface? = null
    private var encoderThread: Thread? = null
    
    override suspend fun initialize() {
        val config = provider.staticConfig ?: throw IllegalStateException("No static config for SRT")
        
        // Initialize SRT socket
        srtSocket = SrtSocket().apply {
            val srtSettings = config.srtSettings ?: SrtSettings()
            
            // Configure SRT options
            setSockOpt(SrtSocketOptions.SRTO_LATENCY, srtSettings.latency)
            setSockOpt(SrtSocketOptions.SRTO_MAXBW, srtSettings.maxBandwidth)
            setSockOpt(SrtSocketOptions.SRTO_CONNTIMEO, srtSettings.connectionTimeout)
            setSockOpt(SrtSocketOptions.SRTO_PEERIDLETIMEO, srtSettings.peerIdleTimeout)
            
            // Set encryption if enabled
            if (srtSettings.enableEncryption && !srtSettings.passphrase.isNullOrEmpty()) {
                setSockOpt(SrtSocketOptions.SRTO_PASSPHRASE, srtSettings.passphrase)
                setSockOpt(SrtSocketOptions.SRTO_PBKEYLEN, srtSettings.keyLength)
            }
        }
        
        // Initialize video encoder
        setupVideoEncoder()
    }
    
    private fun setupVideoEncoder() {
        try {
            // Create H.264 encoder
            mediaCodec = MediaCodec.createEncoderByType(MediaFormat.MIMETYPE_VIDEO_AVC)
            
            val format = MediaFormat.createVideoFormat(
                MediaFormat.MIMETYPE_VIDEO_AVC,
                cameraSettings.resolution.width,
                cameraSettings.resolution.height
            ).apply {
                setInteger(MediaFormat.KEY_COLOR_FORMAT, MediaCodecInfo.CodecCapabilities.COLOR_FormatSurface)
                setInteger(MediaFormat.KEY_BIT_RATE, cameraSettings.bitrate * 1000)
                setInteger(MediaFormat.KEY_FRAME_RATE, cameraSettings.frameRate)
                setInteger(MediaFormat.KEY_I_FRAME_INTERVAL, 1)
            }
            
            mediaCodec?.configure(format, null, null, MediaCodec.CONFIGURE_FLAG_ENCODE)
            inputSurface = mediaCodec?.createInputSurface()
            
        } catch (e: Exception) {
            Log.e(TAG, "Error setting up video encoder", e)
            throw e
        }
    }
    
    override fun getInputSurface(): Surface? = inputSurface
    
    override fun startStreaming(): Boolean {
        val config = provider.staticConfig ?: return false
        
        return try {
            // Connect to SRT server
            val srtUrl = buildSrtUrl(config)
            val host = config.serverUrl
            val port = config.port
            
            srtSocket?.connect(host, port)
            
            // Start encoder
            mediaCodec?.start()
            
            // Start encoding thread
            startEncodingThread()
            
            isStreaming = true
            startTime = System.currentTimeMillis()
            
            Log.d(TAG, "SRT streaming started to $srtUrl")
            true
            
        } catch (e: Exception) {
            Log.e(TAG, "Error starting SRT stream", e)
            false
        }
    }
    
    private fun startEncodingThread() {
        encoderThread = Thread {
            val bufferInfo = MediaCodec.BufferInfo()
            
            while (isStreaming) {
                try {
                    val outputBufferIndex = mediaCodec?.dequeueOutputBuffer(bufferInfo, 10000) ?: -1
                    
                    if (outputBufferIndex >= 0) {
                        val outputBuffer = mediaCodec?.getOutputBuffer(outputBufferIndex)
                        
                        if (outputBuffer != null && bufferInfo.size > 0) {
                            // Send encoded data over SRT
                            val data = ByteArray(bufferInfo.size)
                            outputBuffer.get(data)
                            
                            srtSocket?.send(data)
                        }
                        
                        mediaCodec?.releaseOutputBuffer(outputBufferIndex, false)
                    }
                    
                } catch (e: Exception) {
                    Log.e(TAG, "Error in encoding thread", e)
                    break
                }
            }
        }
        
        encoderThread?.start()
    }
    
    private fun buildSrtUrl(config: StaticProviderConfig): String {
        val srtSettings = config.srtSettings ?: SrtSettings()
        var url = "srt://${config.serverUrl}:${config.port}"
        
        val params = mutableListOf<String>()
        
        when (srtSettings.mode) {
            SrtMode.CALLER -> params.add("mode=caller")
            SrtMode.LISTENER -> params.add("mode=listener")
            SrtMode.RENDEZVOUS -> params.add("mode=rendezvous")
        }
        
        params.add("latency=${srtSettings.latency}")
        
        if (srtSettings.enableEncryption && !srtSettings.passphrase.isNullOrEmpty()) {
            params.add("passphrase=${srtSettings.passphrase}")
            params.add("pbkeylen=${srtSettings.keyLength}")
        }
        
        if (params.isNotEmpty()) {
            url += "?" + params.joinToString("&")
        }
        
        return url
    }
    
    override fun stopStreaming() {
        isStreaming = false
        
        try {
            encoderThread?.interrupt()
            encoderThread?.join(1000)
            
            mediaCodec?.stop()
            mediaCodec?.release()
            mediaCodec = null
            
            srtSocket?.close()
            srtSocket = null
            
            inputSurface = null
            
        } catch (e: Exception) {
            Log.e(TAG, "Error stopping SRT stream", e)
        }
    }
    
    override fun updateSettings(cameraSettings: CameraSettings) {
        // For SRT, we'd need to restart the encoder to change settings
        // This is a limitation of MediaCodec
    }
    
    override fun getStats(): StreamingStats {
        val duration = if (isStreaming) System.currentTimeMillis() - startTime else 0
        return StreamingStats(
            isConnected = isStreaming,
            duration = duration,
            bitrate = if (isStreaming) cameraSettings.bitrate else 0,
            frameRate = if (isStreaming) cameraSettings.frameRate else 0,
            resolution = "${cameraSettings.resolution.width}x${cameraSettings.resolution.height}",
            droppedFrames = 0, // Could be implemented with SRT statistics
            networkType = "SRT"
        )
    }
    
    override fun release() {
        stopStreaming()
    }
}

// WebRTC Streaming Engine for WHIP
class WebRtcStreamingEngine(
    private val context: Context,
    private val provider: StreamProvider,
    private val cameraSettings: CameraSettings
) : StreamingEngineInterface {
    
    private val TAG = "WebRtcStreamingEngine"
    
    private var peerConnectionFactory: PeerConnectionFactory? = null
    private var peerConnection: PeerConnection? = null
    private var localVideoTrack: VideoTrack? = null
    private var localAudioTrack: AudioTrack? = null
    private var videoCapturer: VideoCapturer? = null
    private var videoSource: VideoSource? = null
    private var audioSource: AudioSource? = null
    
    private var isStreaming = false
    private var startTime = 0L
    
    override suspend fun initialize() {
        initializeWebRTC()
        setupPeerConnection()
        setupLocalTracks()
    }
    
    private fun initializeWebRTC() {
        val initializationOptions = PeerConnectionFactory.InitializationOptions.builder(context)
            .createInitializationOptions()
        PeerConnectionFactory.initialize(initializationOptions)
        
        val options = PeerConnectionFactory.Options()
        peerConnectionFactory = PeerConnectionFactory.builder()
            .setOptions(options)
            .createPeerConnectionFactory()
    }
    
    private fun setupPeerConnection() {
        val config = provider.staticConfig ?: return
        val whipSettings = config.whipSettings ?: WhipSettings()
        
        val rtcConfig = PeerConnection.RTCConfiguration(
            whipSettings.iceServers.map { iceServer ->
                PeerConnection.IceServer.builder(iceServer.urls)
                    .setUsername(iceServer.username ?: "")
                    .setPassword(iceServer.credential ?: "")
                    .createIceServer()
            }
        )
        
        peerConnection = peerConnectionFactory?.createPeerConnection(
            rtcConfig,
            object : PeerConnection.Observer {
                override fun onSignalingChange(state: PeerConnection.SignalingState?) {
                    Log.d(TAG, "Signaling state changed: $state")
                }
                
                override fun onIceConnectionChange(state: PeerConnection.IceConnectionState?) {
                    Log.d(TAG, "ICE connection state changed: $state")
                    isStreaming = state == PeerConnection.IceConnectionState.CONNECTED
                }
                
                override fun onIceGatheringChange(state: PeerConnection.IceGatheringState?) {
                    Log.d(TAG, "ICE gathering state changed: $state")
                }
                
                override fun onIceCandidate(candidate: IceCandidate?) {
                    Log.d(TAG, "ICE candidate: $candidate")
                }
                
                override fun onIceCandidatesRemoved(candidates: Array<out IceCandidate>?) {
                    Log.d(TAG, "ICE candidates removed")
                }
                
                override fun onAddStream(stream: MediaStream?) {
                    Log.d(TAG, "Stream added")
                }
                
                override fun onRemoveStream(stream: MediaStream?) {
                    Log.d(TAG, "Stream removed")
                }
                
                override fun onDataChannel(channel: DataChannel?) {
                    Log.d(TAG, "Data channel: $channel")
                }
                
                override fun onRenegotiationNeeded() {
                    Log.d(TAG, "Renegotiation needed")
                }
            }
        )
    }
    
    private fun setupLocalTracks() {
        val config = provider.staticConfig ?: return
        val whipSettings = config.whipSettings ?: WhipSettings()
        
        // Setup video track
        if (whipSettings.enableVideo) {
            videoSource = peerConnectionFactory?.createVideoSource(false)
            localVideoTrack = peerConnectionFactory?.createVideoTrack("video", videoSource)
            
            // Setup video capturer (camera)
            videoCapturer = createCameraCapturer()
            videoCapturer?.initialize(
                SurfaceTextureHelper.create("CaptureThread", EglBase.create().eglBaseContext),
                context,
                videoSource?.capturerObserver
            )
        }
        
        // Setup audio track
        if (whipSettings.enableAudio && cameraSettings.audioEnabled) {
            val audioConstraints = MediaConstraints()
            audioSource = peerConnectionFactory?.createAudioSource(audioConstraints)
            localAudioTrack = peerConnectionFactory?.createAudioTrack("audio", audioSource)
        }
        
        // Add tracks to peer connection
        val stream = peerConnectionFactory?.createLocalMediaStream("local_stream")
        localVideoTrack?.let { stream?.addTrack(it) }
        localAudioTrack?.let { stream?.addTrack(it) }
        stream?.let { peerConnection?.addStream(it) }
    }
    
    private fun createCameraCapturer(): VideoCapturer? {
        val enumerator = Camera2Enumerator(context)
        
        for (deviceName in enumerator.deviceNames) {
            if (enumerator.isBackFacing(deviceName)) {
                return enumerator.createCapturer(deviceName, null)
            }
        }
        
        // Fallback to front camera
        for (deviceName in enumerator.deviceNames) {
            if (enumerator.isFrontFacing(deviceName)) {
                return enumerator.createCapturer(deviceName, null)
            }
        }
        
        return null
    }
    
    override fun getInputSurface(): Surface? {
        // For WebRTC, we don't use a surface directly
        // The camera capturer handles video input
        return null
    }
    
    override fun startStreaming(): Boolean {
        return try {
            videoCapturer?.startCapture(
                cameraSettings.resolution.width,
                cameraSettings.resolution.height,
                cameraSettings.frameRate
            )
            
            // Create offer and start WHIP handshake
            createOfferAndStartWhip()
            
            startTime = System.currentTimeMillis()
            true
        } catch (e: Exception) {
            Log.e(TAG, "Error starting WebRTC stream", e)
            false
        }
    }
    
    private fun createOfferAndStartWhip() {
        val constraints = MediaConstraints().apply {
            mandatory.add(MediaConstraints.KeyValuePair("OfferToReceiveAudio", "false"))
            mandatory.add(MediaConstraints.KeyValuePair("OfferToReceiveVideo", "false"))
        }
        
        peerConnection?.createOffer(object : SdpObserver {
            override fun onCreateSuccess(sdp: SessionDescription?) {
                peerConnection?.setLocalDescription(object : SdpObserver {
                    override fun onSetSuccess() {
                        // Send offer to WHIP endpoint
                        sdp?.let { sendWhipOffer(it) }
                    }
                    
                    override fun onSetFailure(error: String?) {
                        Log.e(TAG, "Failed to set local description: $error")
                    }
                    
                    override fun onCreateSuccess(sdp: SessionDescription?) {}
                    override fun onCreateFailure(error: String?) {}
                }, sdp)
            }
            
            override fun onSetSuccess() {}
            override fun onCreateFailure(error: String?) {
                Log.e(TAG, "Failed to create offer: $error")
            }
            override fun onSetFailure(error: String?) {}
        }, constraints)
    }
    
    private fun sendWhipOffer(offer: SessionDescription) {
        // This would implement the WHIP protocol
        // Send HTTP POST to WHIP endpoint with SDP offer
        // Handle the response with SDP answer

        Log.d(TAG, "Would send WHIP offer: ${offer.description}")
    }
    
    override fun stopStreaming() {
        try {
            videoCapturer?.stopCapture()
            peerConnection?.close()
            isStreaming = false
        } catch (e: Exception) {
            Log.e(TAG, "Error stopping WebRTC stream", e)
        }
    }
    
    override fun updateSettings(cameraSettings: CameraSettings) {
        // Update video capturer settings
        videoCapturer?.changeCaptureFormat(
            cameraSettings.resolution.width,
            cameraSettings.resolution.height,
            cameraSettings.frameRate
        )
    }
    
    override fun getStats(): StreamingStats {
        val duration = if (isStreaming) System.currentTimeMillis() - startTime else 0
        return StreamingStats(
            isConnected = isStreaming,
            duration = duration,
            bitrate = if (isStreaming) cameraSettings.bitrate else 0,
            frameRate = if (isStreaming) cameraSettings.frameRate else 0,
            resolution = "${cameraSettings.resolution.width}x${cameraSettings.resolution.height}",
            droppedFrames = 0,
            networkType = "WebRTC"
        )
    }
    
    override fun release() {
        stopStreaming()
        localVideoTrack?.dispose()
        localAudioTrack?.dispose()
        videoSource?.dispose()
        audioSource?.dispose()
        peerConnection?.dispose()
        peerConnectionFactory?.dispose()
    }
}

enum class StreamingState {
    IDLE,
    CONNECTING,
    STREAMING,
    ERROR
}

data class StreamingStats(
    val isConnected: Boolean = false,
    val duration: Long = 0,
    val bitrate: Int = 0,
    val frameRate: Int = 0,
    val resolution: String = "",
    val droppedFrames: Int = 0,
    val networkType: String = "Unknown"
) 