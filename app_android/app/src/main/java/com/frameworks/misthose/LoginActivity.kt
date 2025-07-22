package com.frameworks.misthose

import android.content.Intent
import android.os.Bundle
import android.view.View
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import com.frameworks.misthose.auth.AuthRepository
import com.frameworks.misthose.databinding.ActivityLoginBinding
import kotlinx.coroutines.launch

class LoginActivity : AppCompatActivity() {
    
    private lateinit var binding: ActivityLoginBinding
    private lateinit var authRepository: AuthRepository
    
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityLoginBinding.inflate(layoutInflater)
        setContentView(binding.root)
        
        authRepository = AuthRepository(this)
        
        setupUI()
        checkExistingAuth()
    }
    
    private fun setupUI() {
        binding.loginButton.setOnClickListener {
            val email = binding.emailInput.text.toString().trim()
            val password = binding.passwordInput.text.toString().trim()
            
            if (email.isEmpty() || password.isEmpty()) {
                Toast.makeText(this, "Please enter email and password", Toast.LENGTH_SHORT).show()
                return@setOnClickListener
            }
            
            performLogin(email, password)
        }
        
        binding.registerButton.setOnClickListener {
            val email = binding.emailInput.text.toString().trim()
            val password = binding.passwordInput.text.toString().trim()
            val name = binding.nameInput.text.toString().trim()
            
            if (email.isEmpty() || password.isEmpty() || name.isEmpty()) {
                Toast.makeText(this, "Please fill in all fields", Toast.LENGTH_SHORT).show()
                return@setOnClickListener
            }
            
            performRegister(email, password, name)
        }
        
        binding.toggleModeButton.setOnClickListener {
            toggleMode()
        }
        
        binding.customServerButton.setOnClickListener {
            showCustomServerDialog()
        }
        
        // Default to login mode
        setLoginMode(true)
    }
    
    private fun checkExistingAuth() {
        lifecycleScope.launch {
            authRepository.authState.collect { authState ->
                if (authState.isAuthenticated) {
                    startMainActivity()
                }
            }
        }
    }
    
    private fun performLogin(email: String, password: String) {
        setLoading(true)
        
        lifecycleScope.launch {
            val result = authRepository.login(email, password)
            
            setLoading(false)
            
            if (result.isSuccess) {
                startMainActivity()
            } else {
                val error = result.exceptionOrNull()?.message ?: "Login failed"
                Toast.makeText(this@LoginActivity, error, Toast.LENGTH_LONG).show()
            }
        }
    }
    
    private fun performRegister(email: String, password: String, name: String) {
        setLoading(true)
        
        lifecycleScope.launch {
            val result = authRepository.register(email, password, name)
            
            setLoading(false)
            
            if (result.isSuccess) {
                startMainActivity()
            } else {
                val error = result.exceptionOrNull()?.message ?: "Registration failed"
                Toast.makeText(this@LoginActivity, error, Toast.LENGTH_LONG).show()
            }
        }
    }
    
    private fun toggleMode() {
        val isLoginMode = binding.nameInput.visibility == View.GONE
        setLoginMode(!isLoginMode)
    }
    
    private fun setLoginMode(isLogin: Boolean) {
        if (isLogin) {
            binding.nameInput.visibility = View.GONE
            binding.loginButton.visibility = View.VISIBLE
            binding.registerButton.visibility = View.GONE
            binding.toggleModeButton.text = "Need an account? Register"
            binding.titleText.text = "Login to FrameWorks"
        } else {
            binding.nameInput.visibility = View.VISIBLE
            binding.loginButton.visibility = View.GONE
            binding.registerButton.visibility = View.VISIBLE
            binding.toggleModeButton.text = "Have an account? Login"
            binding.titleText.text = "Register for FrameWorks"
        }
    }
    
    private fun setLoading(isLoading: Boolean) {
        binding.loginButton.isEnabled = !isLoading
        binding.registerButton.isEnabled = !isLoading
        binding.toggleModeButton.isEnabled = !isLoading
        binding.customServerButton.isEnabled = !isLoading
        
        if (isLoading) {
            binding.progressBar.visibility = View.VISIBLE
        } else {
            binding.progressBar.visibility = View.GONE
        }
    }
    
    private fun showCustomServerDialog() {
        val builder = androidx.appcompat.app.AlertDialog.Builder(this)
        val input = android.widget.EditText(this)
        input.hint = "https://api.your-server.com"
        
        builder.setTitle("Custom Server URL")
            .setMessage("Enter your custom FrameWorks API URL:")
            .setView(input)
            .setPositiveButton("Save") { _, _ ->
                val url = input.text.toString().trim()
                if (url.isNotEmpty()) {
                    authRepository.updateApiBaseUrl(url)
                    Toast.makeText(this, "Server URL updated", Toast.LENGTH_SHORT).show()
                }
            }
            .setNegativeButton("Cancel", null)
            .show()
    }
    
    private fun startMainActivity() {
        val intent = Intent(this, MainActivity::class.java)
        startActivity(intent)
        finish()
    }
} 