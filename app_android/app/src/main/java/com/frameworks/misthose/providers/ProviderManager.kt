package com.frameworks.misthose.providers

import android.content.Context
import android.content.SharedPreferences
import com.frameworks.misthose.api.ApiClient
import com.frameworks.misthose.api.FrameWorksApi
import com.frameworks.misthose.models.*
import com.google.gson.Gson
import com.google.gson.reflect.TypeToken
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import retrofit2.Retrofit
import retrofit2.converter.gson.GsonConverterFactory
import okhttp3.OkHttpClient
import okhttp3.logging.HttpLoggingInterceptor
import android.util.Log

class ProviderManager(private val context: Context) {
    
    private val TAG = "ProviderManager"
    private val PREFS_NAME = "stream_providers"
    private val KEY_PROVIDERS = "providers"
    private val KEY_SELECTED_PROVIDER = "selected_provider"
    
    private val prefs: SharedPreferences = context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
    private val gson = Gson()
    
    // Available providers
    private val _providers = MutableStateFlow<List<StreamProvider>>(emptyList())
    val providers: StateFlow<List<StreamProvider>> = _providers.asStateFlow()
    
    // Currently selected provider
    private val _selectedProvider = MutableStateFlow<StreamProvider?>(null)
    val selectedProvider: StateFlow<StreamProvider?> = _selectedProvider.asStateFlow()
    
    // Service APIs for different providers
    private val serviceApis = mutableMapOf<String, Any>()
    
    init {
        loadProviders()
    }
    
    private fun loadProviders() {
        val savedProviders = getSavedProviders().toMutableList()
        
        // Add default FrameWorks provider if not exists
        if (savedProviders.none { it.id == DefaultProviders.frameworks.id }) {
            savedProviders.add(0, DefaultProviders.frameworks)
        }
        
        _providers.value = savedProviders
        
        // Load selected provider
        val selectedId = prefs.getString(KEY_SELECTED_PROVIDER, DefaultProviders.frameworks.id)
        _selectedProvider.value = savedProviders.find { it.id == selectedId } ?: DefaultProviders.frameworks
    }
    
    private fun getSavedProviders(): List<StreamProvider> {
        val json = prefs.getString(KEY_PROVIDERS, "[]") ?: "[]"
        val type = object : TypeToken<List<StreamProvider>>() {}.type
        return try {
            gson.fromJson(json, type) ?: emptyList()
        } catch (e: Exception) {
            Log.e(TAG, "Error loading providers", e)
            emptyList()
        }
    }
    
    private fun saveProviders() {
        val json = gson.toJson(_providers.value)
        prefs.edit().putString(KEY_PROVIDERS, json).apply()
    }
    
    fun addProvider(provider: StreamProvider) {
        val currentProviders = _providers.value.toMutableList()
        
        // Remove existing provider with same ID
        currentProviders.removeAll { it.id == provider.id }
        
        // Add new provider
        currentProviders.add(provider)
        
        _providers.value = currentProviders
        saveProviders()
    }
    
    fun removeProvider(providerId: String) {
        if (providerId == DefaultProviders.frameworks.id) {
            Log.w(TAG, "Cannot remove default FrameWorks provider")
            return
        }
        
        val currentProviders = _providers.value.toMutableList()
        currentProviders.removeAll { it.id == providerId }
        
        _providers.value = currentProviders
        saveProviders()
        
        // If removed provider was selected, switch to default
        if (_selectedProvider.value?.id == providerId) {
            selectProvider(DefaultProviders.frameworks.id)
        }
    }
    
    fun selectProvider(providerId: String) {
        val provider = _providers.value.find { it.id == providerId }
        if (provider != null) {
            _selectedProvider.value = provider
            prefs.edit().putString(KEY_SELECTED_PROVIDER, providerId).apply()
        }
    }
    
    fun updateProvider(provider: StreamProvider) {
        val currentProviders = _providers.value.toMutableList()
        val index = currentProviders.indexOfFirst { it.id == provider.id }
        
        if (index >= 0) {
            currentProviders[index] = provider
            _providers.value = currentProviders
            saveProviders()
            
            // Update selected provider if it's the same
            if (_selectedProvider.value?.id == provider.id) {
                _selectedProvider.value = provider
            }
        }
    }
    
    // Create static providers
    fun createStaticSrtProvider(name: String, serverUrl: String, port: Int = 9999): StreamProvider {
        return DefaultProviders.createStaticSrtProvider(name, serverUrl, port)
    }
    
    fun createStaticWhipProvider(name: String, serverUrl: String, bearerToken: String? = null): StreamProvider {
        return DefaultProviders.createStaticWhipProvider(name, serverUrl, bearerToken)
    }
    
    fun createCustomServiceProvider(
        name: String,
        baseUrl: String,
        authType: AuthType,
        endpoints: ServiceEndpoints = ServiceEndpoints(),
        authConfig: AuthConfig? = null
    ): StreamProvider {
        return StreamProvider(
            id = "service_${System.currentTimeMillis()}",
            name = name,
            type = ProviderType.CUSTOM_SERVICE,
            serviceConfig = ServiceProviderConfig(
                baseUrl = baseUrl,
                authType = authType,
                endpoints = endpoints,
                authConfig = authConfig
            )
        )
    }
    
    // Service API management
    suspend fun getServiceApi(provider: StreamProvider): Any? {
        if (provider.type == ProviderType.STATIC) return null
        
        val config = provider.serviceConfig ?: return null
        
        return serviceApis.getOrPut(provider.id) {
            createServiceApi(config)
        }
    }
    
    private fun createServiceApi(config: ServiceProviderConfig): Any {
        val loggingInterceptor = HttpLoggingInterceptor().apply {
            level = HttpLoggingInterceptor.Level.BODY
        }
        
        val httpClient = OkHttpClient.Builder()
            .addInterceptor(loggingInterceptor)
            .addInterceptor { chain ->
                val request = chain.request().newBuilder()
                
                // Add authentication headers based on auth type
                config.authConfig?.let { auth ->
                    when (config.authType) {
                        AuthType.JWT -> {
                            auth.token?.let { token ->
                                request.addHeader("Authorization", "Bearer $token")
                            }
                        }
                        AuthType.API_KEY -> {
                            auth.apiKey?.let { key ->
                                request.addHeader("X-API-Key", key)
                            }
                        }
                        AuthType.BASIC -> {
                            if (auth.username != null && auth.password != null) {
                                val credentials = "${auth.username}:${auth.password}"
                                val encoded = android.util.Base64.encodeToString(
                                    credentials.toByteArray(),
                                    android.util.Base64.NO_WRAP
                                )
                                request.addHeader("Authorization", "Basic $encoded")
                            }
                        }
                        else -> { /* No auth or OAuth handled separately */ }
                    }
                }
                
                chain.proceed(request.build())
            }
            .build()
        
        val retrofit = Retrofit.Builder()
            .baseUrl(config.baseUrl)
            .client(httpClient)
            .addConverterFactory(GsonConverterFactory.create())
            .build()
        
        // For FrameWorks providers, return FrameWorksApi
        return if (config.baseUrl.contains("frameworks")) {
            retrofit.create(FrameWorksApi::class.java)
        } else {
            // For custom services, return a generic API interface
            retrofit.create(GenericServiceApi::class.java)
        }
    }
    
    // Authentication for service providers
    suspend fun authenticateProvider(provider: StreamProvider, username: String, password: String): Boolean {
        val config = provider.serviceConfig ?: return false
        
        return try {
            when (provider.type) {
                ProviderType.FRAMEWORKS -> {
                    val api = getServiceApi(provider) as? FrameWorksApi ?: return false
                    val response = api.login(mapOf("username" to username, "password" to password))
                    
                    if (response.isSuccessful) {
                        val authResponse = response.body()
                        authResponse?.token?.let { token ->
                            // Update provider with auth token
                            val updatedConfig = config.copy(
                                authConfig = config.authConfig?.copy(
                                    username = username,
                                    token = token,
                                    refreshToken = authResponse.refreshToken,
                                    tokenExpiry = System.currentTimeMillis() + (authResponse.expiresIn * 1000)
                                ) ?: AuthConfig(
                                    username = username,
                                    token = token,
                                    refreshToken = authResponse.refreshToken,
                                    tokenExpiry = System.currentTimeMillis() + (authResponse.expiresIn * 1000)
                                )
                            )
                            
                            updateProvider(provider.copy(serviceConfig = updatedConfig))
                            true
                        } ?: false
                    } else {
                        false
                    }
                }
                ProviderType.CUSTOM_SERVICE -> {
                    // Handle custom service authentication
                    val api = getServiceApi(provider) as? GenericServiceApi ?: return false
                    val response = api.login(mapOf("username" to username, "password" to password))
                    
                    response.isSuccessful
                }
                else -> false
            }
        } catch (e: Exception) {
            Log.e(TAG, "Authentication failed for provider ${provider.name}", e)
            false
        }
    }
    
    // Get streaming URL for service providers
    suspend fun getStreamingUrl(provider: StreamProvider, streamId: String? = null): String? {
        if (provider.type == ProviderType.STATIC) {
            val config = provider.staticConfig ?: return null
            return when (config.protocol) {
                StreamProtocol.SRT -> {
                    "srt://${config.serverUrl}:${config.port}"
                }
                StreamProtocol.WHIP -> {
                    "${config.serverUrl}/whip"
                }
            }
        }
        
        // For service providers, get streaming URL from API
        val config = provider.serviceConfig ?: return null
        
        return try {
            when (provider.type) {
                ProviderType.FRAMEWORKS -> {
                    val api = getServiceApi(provider) as? FrameWorksApi ?: return null
                    val response = api.getStreams()
                    
                    if (response.isSuccessful) {
                        val streams = response.body()
                        // Return the first available stream URL or create a new one
                        streams?.firstOrNull()?.streamUrl
                    } else {
                        null
                    }
                }
                ProviderType.CUSTOM_SERVICE -> {
                    // Handle custom service streaming URL
                    val api = getServiceApi(provider) as? GenericServiceApi ?: return null
                    val response = api.getStreamingUrl(streamId ?: "default")
                    
                    if (response.isSuccessful) {
                        response.body()?.get("url")
                    } else {
                        null
                    }
                }
                else -> null
            }
        } catch (e: Exception) {
            Log.e(TAG, "Failed to get streaming URL for provider ${provider.name}", e)
            null
        }
    }
}

// Generic API interface for custom services
interface GenericServiceApi {
    @retrofit2.http.POST("/auth/login")
    suspend fun login(@retrofit2.http.Body credentials: Map<String, String>): retrofit2.Response<Map<String, Any>>
    
    @retrofit2.http.GET("/api/streaming/{streamId}")
    suspend fun getStreamingUrl(@retrofit2.http.Path("streamId") streamId: String): retrofit2.Response<Map<String, String>>
    
    @retrofit2.http.GET("/api/streams")
    suspend fun getStreams(): retrofit2.Response<List<Map<String, Any>>>
} 