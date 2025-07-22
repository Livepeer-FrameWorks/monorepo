package com.frameworks.misthose

import android.Manifest
import android.content.pm.PackageManager
import android.os.Bundle
import android.util.Log
import android.view.View
import android.widget.*
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.camera.view.PreviewView
import androidx.core.content.ContextCompat
import androidx.lifecycle.lifecycleScope
import com.frameworks.misthose.adapters.ProviderListAdapter
import com.frameworks.misthose.auth.AuthRepository
import com.frameworks.misthose.camera.CameraController
import com.frameworks.misthose.models.*
import com.frameworks.misthose.providers.ProviderManager
import com.frameworks.misthose.streaming.StreamingEngine
import com.frameworks.misthose.streaming.StreamingState
import com.google.android.material.bottomsheet.BottomSheetDialog
import com.google.android.material.button.MaterialButton
import com.google.android.material.slider.Slider
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.launch

class MainActivity : AppCompatActivity() {

    private val TAG = "MainActivity"
    
    // Core components
    private lateinit var authRepository: AuthRepository
    private lateinit var cameraController: CameraController
    private lateinit var streamingEngine: StreamingEngine
    private lateinit var providerManager: ProviderManager
    
    // UI Components
    private lateinit var previewView: PreviewView
    private lateinit var streamButton: MaterialButton
    private lateinit var settingsButton: MaterialButton
    private lateinit var providerButton: MaterialButton
    private lateinit var switchCameraButton: MaterialButton
    private lateinit var statusText: TextView
    private lateinit var statsText: TextView
    
    // Current settings
    private var currentCameraSettings = CameraSettings()
    
    // Permission launcher
    private val permissionLauncher = registerForActivityResult(
        ActivityResultContracts.RequestMultiplePermissions()
    ) { permissions ->
        val allGranted = permissions.values.all { it }
        if (allGranted) {
            initializeCamera()
        } else {
            showPermissionDeniedDialog()
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        
        initializeComponents()
        setupUI()
        checkPermissions()
    }
    
    private fun initializeComponents() {
        authRepository = AuthRepository(this)
        cameraController = CameraController(this)
        streamingEngine = StreamingEngine(this)
        providerManager = ProviderManager(this)
    }
    
    private fun setupUI() {
        previewView = findViewById(R.id.previewView)
        streamButton = findViewById(R.id.streamButton)
        settingsButton = findViewById(R.id.settingsButton)
        providerButton = findViewById(R.id.providerButton)
        switchCameraButton = findViewById(R.id.switchCameraButton)
        statusText = findViewById(R.id.statusText)
        statsText = findViewById(R.id.statsText)
        
        // Stream button
        streamButton.setOnClickListener {
            toggleStreaming()
        }
        
        // Settings button
        settingsButton.setOnClickListener {
            showCameraSettings()
        }
        
        // Provider button
        providerButton.setOnClickListener {
            showProviderSelection()
        }
        
        // Switch camera button
        switchCameraButton.setOnClickListener {
            cameraController.switchCamera()
        }
        
        // Observe streaming state
        lifecycleScope.launch {
            streamingEngine.streamingState.collectLatest { state ->
                updateStreamingUI(state)
            }
        }
        
        // Observe streaming stats
        lifecycleScope.launch {
            streamingEngine.streamingStats.collectLatest { stats ->
                updateStatsUI(stats)
            }
        }
        
        // Observe selected provider
        lifecycleScope.launch {
            providerManager.selectedProvider.collectLatest { provider ->
                updateProviderUI(provider)
            }
        }
        
        // Observe camera settings
        lifecycleScope.launch {
            cameraController.cameraSettings.collectLatest { settings ->
                currentCameraSettings = settings
            }
        }
    }
    
    private fun checkPermissions() {
        val requiredPermissions = arrayOf(
            Manifest.permission.CAMERA,
            Manifest.permission.RECORD_AUDIO,
            Manifest.permission.WRITE_EXTERNAL_STORAGE
        )
        
        val missingPermissions = requiredPermissions.filter {
            ContextCompat.checkSelfPermission(this, it) != PackageManager.PERMISSION_GRANTED
        }
        
        if (missingPermissions.isNotEmpty()) {
            permissionLauncher.launch(missingPermissions.toTypedArray())
        } else {
            initializeCamera()
        }
    }
    
    private fun showPermissionDeniedDialog() {
        AlertDialog.Builder(this)
            .setTitle("Permissions Required")
            .setMessage("Camera and microphone permissions are required for streaming.")
            .setPositiveButton("Grant") { _, _ ->
                checkPermissions()
            }
            .setNegativeButton("Cancel") { _, _ ->
                finish()
            }
            .show()
    }
    
    private fun initializeCamera() {
        lifecycleScope.launch {
            try {
                cameraController.initializeCamera(this@MainActivity, previewView)
                Log.d(TAG, "Camera initialized successfully")
            } catch (e: Exception) {
                Log.e(TAG, "Failed to initialize camera", e)
                showErrorDialog("Failed to initialize camera: ${e.message}")
            }
        }
    }
    
    private fun toggleStreaming() {
        lifecycleScope.launch {
            when (streamingEngine.streamingState.value) {
                StreamingState.IDLE -> {
                    startStreaming()
                }
                StreamingState.STREAMING -> {
                    stopStreaming()
                }
                else -> {
                    // Do nothing if connecting or in error state
                }
            }
        }
    }
    
    private suspend fun startStreaming() {
        val provider = providerManager.selectedProvider.value
        if (provider == null) {
            showErrorDialog("No streaming provider selected")
            return
        }
        
        // Check authentication for service providers
        if (provider.type != ProviderType.STATIC) {
            val config = provider.serviceConfig
            if (config?.authConfig?.token == null) {
                showLoginDialog(provider)
                return
            }
        }
        
        val success = streamingEngine.startStreaming(provider, currentCameraSettings)
        if (!success) {
            showErrorDialog("Failed to start streaming")
        }
    }
    
    private fun stopStreaming() {
        streamingEngine.stopStreaming()
    }
    
    private fun updateStreamingUI(state: StreamingState) {
        runOnUiThread {
            when (state) {
                StreamingState.IDLE -> {
                    streamButton.text = "Start Stream"
                    streamButton.isEnabled = true
                    statusText.text = "Ready to stream"
                    statusText.setTextColor(ContextCompat.getColor(this, R.color.tokyo_night_fg_dark))
                }
                StreamingState.CONNECTING -> {
                    streamButton.text = "Connecting..."
                    streamButton.isEnabled = false
                    statusText.text = "Connecting to server..."
                    statusText.setTextColor(ContextCompat.getColor(this, R.color.tokyo_night_yellow))
                }
                StreamingState.STREAMING -> {
                    streamButton.text = "Stop Stream"
                    streamButton.isEnabled = true
                    statusText.text = "Live streaming"
                    statusText.setTextColor(ContextCompat.getColor(this, R.color.tokyo_night_green))
                }
                StreamingState.ERROR -> {
                    streamButton.text = "Start Stream"
                    streamButton.isEnabled = true
                    statusText.text = "Streaming error"
                    statusText.setTextColor(ContextCompat.getColor(this, R.color.tokyo_night_red))
                }
            }
        }
    }
    
    private fun updateStatsUI(stats: com.frameworks.misthose.streaming.StreamingStats) {
        runOnUiThread {
            if (stats.isConnected) {
                val duration = formatDuration(stats.duration)
                val statsString = "Duration: $duration\n" +
                        "Bitrate: ${stats.bitrate} kbps\n" +
                        "FPS: ${stats.frameRate}\n" +
                        "Resolution: ${stats.resolution}\n" +
                        "Dropped: ${stats.droppedFrames}"
                statsText.text = statsString
                statsText.visibility = View.VISIBLE
            } else {
                statsText.visibility = View.GONE
            }
        }
    }
    
    private fun updateProviderUI(provider: StreamProvider?) {
        runOnUiThread {
            providerButton.text = provider?.name ?: "Select Provider"
        }
    }
    
    private fun formatDuration(millis: Long): String {
        val seconds = millis / 1000
        val minutes = seconds / 60
        val hours = minutes / 60
        
        return when {
            hours > 0 -> String.format("%02d:%02d:%02d", hours, minutes % 60, seconds % 60)
            else -> String.format("%02d:%02d", minutes, seconds % 60)
        }
    }
    
    private fun showCameraSettings() {
        val dialog = BottomSheetDialog(this)
        val view = layoutInflater.inflate(R.layout.dialog_camera_settings, null)
        
        setupCameraSettingsDialog(view)
        
        dialog.setContentView(view)
        dialog.show()
    }
    
    private fun setupCameraSettingsDialog(view: View) {
        // Resolution spinner
        val resolutionSpinner = view.findViewById<Spinner>(R.id.resolutionSpinner)
        val resolutions = Resolution.values()
        val resolutionAdapter = ArrayAdapter(this, android.R.layout.simple_spinner_item, 
            resolutions.map { it.displayName })
        resolutionAdapter.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item)
        resolutionSpinner.adapter = resolutionAdapter
        resolutionSpinner.setSelection(resolutions.indexOf(currentCameraSettings.resolution))
        
        // Frame rate spinner
        val frameRateSpinner = view.findViewById<Spinner>(R.id.frameRateSpinner)
        val frameRates = listOf(15, 24, 30, 60)
        val frameRateAdapter = ArrayAdapter(this, android.R.layout.simple_spinner_item, 
            frameRates.map { "${it} fps" })
        frameRateAdapter.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item)
        frameRateSpinner.adapter = frameRateAdapter
        frameRateSpinner.setSelection(frameRates.indexOf(currentCameraSettings.frameRate))
        
        // Bitrate slider
        val bitrateSlider = view.findViewById<Slider>(R.id.bitrateSlider)
        val bitrateText = view.findViewById<TextView>(R.id.bitrateText)
        bitrateSlider.value = currentCameraSettings.bitrate.toFloat()
        bitrateText.text = "${currentCameraSettings.bitrate} kbps"
        bitrateSlider.addOnChangeListener { _, value, _ ->
            bitrateText.text = "${value.toInt()} kbps"
        }
        
        // Focus mode spinner
        val focusModeSpinner = view.findViewById<Spinner>(R.id.focusModeSpinner)
        val focusModes = FocusMode.values()
        val focusModeAdapter = ArrayAdapter(this, android.R.layout.simple_spinner_item, 
            focusModes.map { it.displayName })
        focusModeAdapter.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item)
        focusModeSpinner.adapter = focusModeAdapter
        focusModeSpinner.setSelection(focusModes.indexOf(currentCameraSettings.focusMode))
        
        // White balance spinner
        val whiteBalanceSpinner = view.findViewById<Spinner>(R.id.whiteBalanceSpinner)
        val whiteBalanceModes = WhiteBalanceMode.values()
        val whiteBalanceAdapter = ArrayAdapter(this, android.R.layout.simple_spinner_item, 
            whiteBalanceModes.map { it.displayName })
        whiteBalanceAdapter.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item)
        whiteBalanceSpinner.adapter = whiteBalanceAdapter
        whiteBalanceSpinner.setSelection(whiteBalanceModes.indexOf(currentCameraSettings.whiteBalanceMode))
        
        // Exposure compensation slider
        val exposureSlider = view.findViewById<Slider>(R.id.exposureSlider)
        val exposureText = view.findViewById<TextView>(R.id.exposureText)
        exposureSlider.value = currentCameraSettings.exposureCompensation.toFloat()
        exposureText.text = "${currentCameraSettings.exposureCompensation} EV"
        exposureSlider.addOnChangeListener { _, value, _ ->
            exposureText.text = "${value.toInt()} EV"
        }
        
        // Zoom slider
        val zoomSlider = view.findViewById<Slider>(R.id.zoomSlider)
        val zoomText = view.findViewById<TextView>(R.id.zoomText)
        zoomSlider.value = currentCameraSettings.zoomLevel
        zoomText.text = "${String.format("%.1f", currentCameraSettings.zoomLevel)}x"
        zoomSlider.addOnChangeListener { _, value, _ ->
            zoomText.text = "${String.format("%.1f", value)}x"
        }
        
        // Torch switch
        val torchSwitch = view.findViewById<Switch>(R.id.torchSwitch)
        torchSwitch.isChecked = currentCameraSettings.torchEnabled
        
        // Audio switch
        val audioSwitch = view.findViewById<Switch>(R.id.audioSwitch)
        audioSwitch.isChecked = currentCameraSettings.audioEnabled
        
        // Apply button
        val applyButton = view.findViewById<MaterialButton>(R.id.applyButton)
        applyButton.setOnClickListener {
            val newSettings = currentCameraSettings.copy(
                resolution = resolutions[resolutionSpinner.selectedItemPosition],
                frameRate = frameRates[frameRateSpinner.selectedItemPosition],
                bitrate = bitrateSlider.value.toInt(),
                focusMode = focusModes[focusModeSpinner.selectedItemPosition],
                whiteBalanceMode = whiteBalanceModes[whiteBalanceSpinner.selectedItemPosition],
                exposureCompensation = exposureSlider.value.toInt(),
                zoomLevel = zoomSlider.value,
                torchEnabled = torchSwitch.isChecked,
                audioEnabled = audioSwitch.isChecked
            )
            
            cameraController.updateCameraSettings(newSettings)
            streamingEngine.updateSettings(newSettings)
        }
    }
    
    private fun showProviderSelection() {
        val dialog = BottomSheetDialog(this)
        val view = layoutInflater.inflate(R.layout.dialog_provider_selection, null)
        
        setupProviderSelectionDialog(view)
        
        dialog.setContentView(view)
        dialog.show()
    }
    
    private fun setupProviderSelectionDialog(view: View) {
        val providerList = view.findViewById<ListView>(R.id.providerList)
        val addProviderButton = view.findViewById<MaterialButton>(R.id.addProviderButton)
        
        // Setup provider list
        lifecycleScope.launch {
            providerManager.providers.collectLatest { providers ->
                val adapter = ProviderListAdapter(this@MainActivity, providers) { provider ->
                    providerManager.selectProvider(provider.id)
                }
                providerList.adapter = adapter
            }
        }
        
        // Add provider button
        addProviderButton.setOnClickListener {
            showAddProviderDialog()
        }
    }
    
    private fun showAddProviderDialog() {
        val options = arrayOf(
            "SRT Server", 
            "WHIP Server",
            "Custom Service"
        )
        
        AlertDialog.Builder(this)
            .setTitle("Add Provider")
            .setItems(options) { _, which ->
                when (which) {
                    0 -> showAddSrtProviderDialog()
                    1 -> showAddWhipProviderDialog()
                    2 -> showAddCustomServiceDialog()
                }
            }
            .show()
    }
    
    private fun showAddSrtProviderDialog() {
        val view = layoutInflater.inflate(R.layout.dialog_add_srt_provider, null)
        
        AlertDialog.Builder(this)
            .setTitle("Add SRT Provider")
            .setView(view)
            .setPositiveButton("Add") { _, _ ->
                val name = view.findViewById<EditText>(R.id.nameEdit).text.toString()
                val url = view.findViewById<EditText>(R.id.urlEdit).text.toString()
                val port = view.findViewById<EditText>(R.id.portEdit).text.toString().toIntOrNull() ?: 9999
                
                if (name.isNotBlank() && url.isNotBlank()) {
                    val provider = providerManager.createStaticSrtProvider(name, url, port)
                    providerManager.addProvider(provider)
                }
            }
            .setNegativeButton("Cancel", null)
            .show()
    }
    
    private fun showAddWhipProviderDialog() {
        val view = layoutInflater.inflate(R.layout.dialog_add_whip_provider, null)
        
        AlertDialog.Builder(this)
            .setTitle("Add WHIP Provider")
            .setView(view)
            .setPositiveButton("Add") { _, _ ->
                val name = view.findViewById<EditText>(R.id.nameEdit).text.toString()
                val url = view.findViewById<EditText>(R.id.urlEdit).text.toString()
                val token = view.findViewById<EditText>(R.id.tokenEdit).text.toString().takeIf { it.isNotBlank() }
                
                if (name.isNotBlank() && url.isNotBlank()) {
                    val provider = providerManager.createStaticWhipProvider(name, url, token)
                    providerManager.addProvider(provider)
                }
            }
            .setNegativeButton("Cancel", null)
            .show()
    }
    
    private fun showAddCustomServiceDialog() {
        val view = layoutInflater.inflate(R.layout.dialog_add_custom_service, null)
        
        AlertDialog.Builder(this)
            .setTitle("Add Custom Service")
            .setView(view)
            .setPositiveButton("Add") { _, _ ->
                val name = view.findViewById<EditText>(R.id.nameEdit).text.toString()
                val url = view.findViewById<EditText>(R.id.urlEdit).text.toString()
                val authSpinner = view.findViewById<Spinner>(R.id.authTypeSpinner)
                val authType = AuthType.values()[authSpinner.selectedItemPosition]
                
                if (name.isNotBlank() && url.isNotBlank()) {
                    val provider = providerManager.createCustomServiceProvider(name, url, authType)
                    providerManager.addProvider(provider)
                }
            }
            .setNegativeButton("Cancel", null)
            .show()
    }
    
    private fun showLoginDialog(provider: StreamProvider) {
        val view = layoutInflater.inflate(R.layout.dialog_login, null)
        
        AlertDialog.Builder(this)
            .setTitle("Login to ${provider.name}")
            .setView(view)
            .setPositiveButton("Login") { _, _ ->
                val username = view.findViewById<EditText>(R.id.usernameEdit).text.toString()
                val password = view.findViewById<EditText>(R.id.passwordEdit).text.toString()
                
                lifecycleScope.launch {
                    val success = providerManager.authenticateProvider(provider, username, password)
                    if (success) {
                        startStreaming()
                    } else {
                        showErrorDialog("Authentication failed")
                    }
                }
            }
            .setNegativeButton("Cancel", null)
            .show()
    }
    
    private fun showErrorDialog(message: String) {
        AlertDialog.Builder(this)
            .setTitle("Error")
            .setMessage(message)
            .setPositiveButton("OK", null)
            .show()
    }
    
    override fun onDestroy() {
        super.onDestroy()
        cameraController.release()
        streamingEngine.release()
    }
} 