package com.frameworks.misthose.models

data class StreamingState(
    val isStreaming: Boolean = false,
    val status: String = "Disconnected",
    val url: String = "",
    val protocol: StreamProtocol = StreamProtocol.RTMP
) 