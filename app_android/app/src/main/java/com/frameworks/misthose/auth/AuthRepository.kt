package com.frameworks.misthose.auth

import android.content.Context
import android.content.SharedPreferences
import com.frameworks.misthose.api.ApiClient
import com.frameworks.misthose.api.LoginRequest
import com.frameworks.misthose.api.RegisterRequest
import com.frameworks.misthose.models.AuthState
import com.frameworks.misthose.models.User
import com.google.gson.Gson
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

class AuthRepository(private val context: Context) {
    
    private val prefs: SharedPreferences = context.getSharedPreferences("auth", Context.MODE_PRIVATE)
    private val gson = Gson()
    
    private val _authState = MutableStateFlow(loadAuthState())
    val authState: StateFlow<AuthState> = _authState.asStateFlow()
    
    init {
        // Update API base URL if custom URL is saved
        _authState.value.apiBaseUrl.let { url ->
            if (url != "https://api.frameworks.dev") {
                ApiClient.updateBaseUrl(url)
            }
        }
    }
    
    suspend fun login(email: String, password: String): Result<AuthState> {
        return try {
            val api = ApiClient.getApi()
            val response = api.login(LoginRequest(email, password))
            
            if (response.isSuccessful) {
                val loginResponse = response.body()!!
                val newAuthState = AuthState(
                    isAuthenticated = true,
                    user = loginResponse.user,
                    token = loginResponse.token,
                    apiBaseUrl = _authState.value.apiBaseUrl
                )
                
                saveAuthState(newAuthState)
                _authState.value = newAuthState
                
                Result.success(newAuthState)
            } else {
                Result.failure(Exception("Login failed: ${response.message()}"))
            }
        } catch (e: Exception) {
            Result.failure(e)
        }
    }
    
    suspend fun register(email: String, password: String, name: String): Result<AuthState> {
        return try {
            val api = ApiClient.getApi()
            val response = api.register(RegisterRequest(email, password, name))
            
            if (response.isSuccessful) {
                val loginResponse = response.body()!!
                val newAuthState = AuthState(
                    isAuthenticated = true,
                    user = loginResponse.user,
                    token = loginResponse.token,
                    apiBaseUrl = _authState.value.apiBaseUrl
                )
                
                saveAuthState(newAuthState)
                _authState.value = newAuthState
                
                Result.success(newAuthState)
            } else {
                Result.failure(Exception("Registration failed: ${response.message()}"))
            }
        } catch (e: Exception) {
            Result.failure(e)
        }
    }
    
    suspend fun refreshUser(): Result<User> {
        return try {
            val currentState = _authState.value
            if (!currentState.isAuthenticated || currentState.token == null) {
                return Result.failure(Exception("Not authenticated"))
            }
            
            val api = ApiClient.getApi()
            val response = api.getCurrentUser("Bearer ${currentState.token}")
            
            if (response.isSuccessful) {
                val user = response.body()!!
                val newAuthState = currentState.copy(user = user)
                
                saveAuthState(newAuthState)
                _authState.value = newAuthState
                
                Result.success(user)
            } else {
                Result.failure(Exception("Failed to refresh user: ${response.message()}"))
            }
        } catch (e: Exception) {
            Result.failure(e)
        }
    }
    
    fun updateApiBaseUrl(newUrl: String) {
        ApiClient.updateBaseUrl(newUrl)
        val newAuthState = _authState.value.copy(apiBaseUrl = newUrl)
        saveAuthState(newAuthState)
        _authState.value = newAuthState
    }
    
    fun logout() {
        clearAuthState()
        _authState.value = AuthState()
    }
    
    private fun loadAuthState(): AuthState {
        val authJson = prefs.getString("auth_state", null)
        return if (authJson != null) {
            try {
                gson.fromJson(authJson, AuthState::class.java)
            } catch (e: Exception) {
                AuthState()
            }
        } else {
            AuthState()
        }
    }
    
    private fun saveAuthState(authState: AuthState) {
        val authJson = gson.toJson(authState)
        prefs.edit().putString("auth_state", authJson).apply()
    }
    
    private fun clearAuthState() {
        prefs.edit().remove("auth_state").apply()
    }
} 