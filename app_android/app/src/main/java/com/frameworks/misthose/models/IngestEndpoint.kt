package com.frameworks.misthose.models

data class IngestEndpoint(
    val name: String,
    val url: String,
    val protocol: StreamProtocol
) 