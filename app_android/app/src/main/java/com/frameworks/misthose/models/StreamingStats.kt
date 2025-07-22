package com.frameworks.misthose.models

data class StreamingStats(
    val bitrate: Int = 0,
    val fps: Int = 0,
    val duration: String = "00:00:00",
    val bytesTransferred: Long = 0,
    val droppedFrames: Int = 0
) 