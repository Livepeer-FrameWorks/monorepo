package com.frameworks.misthose.models

data class AuthState(
    val isAuthenticated: Boolean = false,
    val user: User? = null,
    val token: String? = null,
    val apiBaseUrl: String = "https://api.frameworks.dev"
)

data class User(
    val id: String,
    val email: String,
    val name: String,
    val streamKey: String,
    val plan: String,
    val clusters: List<Cluster> = emptyList()
)

data class Cluster(
    val id: String,
    val name: String,
    val region: String,
    val ingestEndpoints: List<IngestEndpoint>,
    val isDefault: Boolean = false,
    val isCustomer: Boolean = false // true for customer-hosted clusters
) 