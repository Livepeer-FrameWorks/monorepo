package com.frameworks.misthose.camera

import android.annotation.SuppressLint
import android.content.Context
import android.graphics.SurfaceTexture
import android.hardware.camera2.*
import android.hardware.camera2.params.MeteringRectangle
import android.media.MediaRecorder
import android.os.Handler
import android.os.HandlerThread
import android.util.Log
import android.util.Range
import android.util.Size
import android.view.Surface
import androidx.camera.core.*
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.lifecycle.LifecycleOwner
import com.frameworks.misthose.models.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors

class CameraController(private val context: Context) {
    
    private val TAG = "CameraController"
    
    // Camera components
    private var cameraProvider: ProcessCameraProvider? = null
    private var camera: Camera? = null
    private var preview: Preview? = null
    private var videoCapture: VideoCapture<MediaRecorder>? = null
    private var imageCapture: ImageCapture? = null
    
    // Camera2 for manual controls
    private var cameraManager: CameraManager? = null
    private var cameraDevice: CameraDevice? = null
    private var captureSession: CameraCaptureSession? = null
    private var backgroundHandler: Handler? = null
    private var backgroundThread: HandlerThread? = null
    
    // Executor for camera operations
    private val cameraExecutor: ExecutorService = Executors.newSingleThreadExecutor()
    
    // Current settings
    private val _cameraSettings = MutableStateFlow(CameraSettings())
    val cameraSettings: StateFlow<CameraSettings> = _cameraSettings.asStateFlow()
    
    private val _isRecording = MutableStateFlow(false)
    val isRecording: StateFlow<Boolean> = _isRecording.asStateFlow()
    
    private val _availableCameras = MutableStateFlow<List<CameraInfo>>(emptyList())
    val availableCameras: StateFlow<List<CameraInfo>> = _availableCameras.asStateFlow()
    
    private val _currentCameraId = MutableStateFlow<String?>(null)
    val currentCameraId: StateFlow<String?> = _currentCameraId.asStateFlow()
    
    // Camera capabilities
    private var cameraCharacteristics: CameraCharacteristics? = null
    private var supportedResolutions: List<Resolution> = emptyList()
    private var supportedFrameRates: List<Int> = emptyList()
    private var manualControlsSupported: Boolean = false
    
    init {
        cameraManager = context.getSystemService(Context.CAMERA_SERVICE) as CameraManager
        startBackgroundThread()
        detectAvailableCameras()
    }
    
    private fun startBackgroundThread() {
        backgroundThread = HandlerThread("CameraBackground").also { it.start() }
        backgroundHandler = Handler(backgroundThread?.looper!!)
    }
    
    private fun stopBackgroundThread() {
        backgroundThread?.quitSafely()
        try {
            backgroundThread?.join()
            backgroundThread = null
            backgroundHandler = null
        } catch (e: InterruptedException) {
            Log.e(TAG, "Error stopping background thread", e)
        }
    }
    
    @SuppressLint("MissingPermission")
    private fun detectAvailableCameras() {
        try {
            val cameraIds = cameraManager?.cameraIdList ?: return
            val cameras = mutableListOf<CameraInfo>()
            
            for (cameraId in cameraIds) {
                val characteristics = cameraManager?.getCameraCharacteristics(cameraId)
                val facing = characteristics?.get(CameraCharacteristics.LENS_FACING)
                
                val cameraInfo = CameraInfo(
                    id = cameraId,
                    facing = when (facing) {
                        CameraCharacteristics.LENS_FACING_FRONT -> CameraFacing.FRONT
                        CameraCharacteristics.LENS_FACING_BACK -> CameraFacing.BACK
                        else -> CameraFacing.EXTERNAL
                    },
                    displayName = when (facing) {
                        CameraCharacteristics.LENS_FACING_FRONT -> "Front Camera"
                        CameraCharacteristics.LENS_FACING_BACK -> "Back Camera"
                        else -> "External Camera $cameraId"
                    }
                )
                cameras.add(cameraInfo)
            }
            
            _availableCameras.value = cameras
            
            // Set default camera (back camera if available, otherwise first camera)
            val defaultCamera = cameras.find { it.facing == CameraFacing.BACK } ?: cameras.firstOrNull()
            defaultCamera?.let { _currentCameraId.value = it.id }
            
        } catch (e: Exception) {
            Log.e(TAG, "Error detecting cameras", e)
        }
    }
    
    suspend fun initializeCamera(lifecycleOwner: LifecycleOwner, previewView: androidx.camera.view.PreviewView) {
        try {
            cameraProvider = ProcessCameraProvider.getInstance(context).get()
            
            // Setup preview
            preview = Preview.Builder()
                .setTargetResolution(Size(
                    _cameraSettings.value.resolution.width,
                    _cameraSettings.value.resolution.height
                ))
                .build()
            
            preview?.setSurfaceProvider(previewView.surfaceProvider)
            
            // Setup video capture
            val recorder = MediaRecorder.Builder()
                .setVideoSource(MediaRecorder.VideoSource.SURFACE)
                .setAudioSource(MediaRecorder.AudioSource.MIC)
                .setOutputFormat(MediaRecorder.OutputFormat.MPEG_4)
                .setVideoEncoder(MediaRecorder.VideoEncoder.H264)
                .setAudioEncoder(MediaRecorder.AudioEncoder.AAC)
                .setVideoSize(_cameraSettings.value.resolution.width, _cameraSettings.value.resolution.height)
                .setVideoFrameRate(_cameraSettings.value.frameRate)
                .setVideoEncodingBitRate(_cameraSettings.value.bitrate * 1000)
                .build()
            
            videoCapture = VideoCapture.withOutput(recorder)
            
            // Setup image capture
            imageCapture = ImageCapture.Builder()
                .setTargetResolution(Size(
                    _cameraSettings.value.resolution.width,
                    _cameraSettings.value.resolution.height
                ))
                .build()
            
            // Select camera
            val cameraSelector = CameraSelector.Builder()
                .requireLensFacing(
                    when (availableCameras.value.find { it.id == _currentCameraId.value }?.facing) {
                        CameraFacing.FRONT -> CameraSelector.LENS_FACING_FRONT
                        CameraFacing.BACK -> CameraSelector.LENS_FACING_BACK
                        else -> CameraSelector.LENS_FACING_BACK
                    }
                )
                .build()
            
            // Bind use cases
            cameraProvider?.unbindAll()
            camera = cameraProvider?.bindToLifecycle(
                lifecycleOwner,
                cameraSelector,
                preview,
                videoCapture,
                imageCapture
            )
            
            // Initialize manual controls if supported
            initializeManualControls()
            
        } catch (e: Exception) {
            Log.e(TAG, "Error initializing camera", e)
        }
    }
    
    @SuppressLint("MissingPermission")
    private fun initializeManualControls() {
        val cameraId = _currentCameraId.value ?: return
        
        try {
            cameraCharacteristics = cameraManager?.getCameraCharacteristics(cameraId)
            
            // Check if manual controls are supported
            val controlModes = cameraCharacteristics?.get(CameraCharacteristics.CONTROL_AVAILABLE_MODES)
            manualControlsSupported = controlModes?.contains(CameraCharacteristics.CONTROL_MODE_OFF) == true
            
            // Get supported resolutions
            val streamConfigMap = cameraCharacteristics?.get(CameraCharacteristics.SCALER_STREAM_CONFIGURATION_MAP)
            val sizes = streamConfigMap?.getOutputSizes(MediaRecorder::class.java) ?: emptyArray()
            
            supportedResolutions = sizes.mapNotNull { size ->
                Resolution.values().find { it.width == size.width && it.height == size.height }
            }
            
            // Get supported frame rates
            val fpsRanges = cameraCharacteristics?.get(CameraCharacteristics.CONTROL_AE_AVAILABLE_TARGET_FPS_RANGES)
            supportedFrameRates = fpsRanges?.map { it.upper }?.distinct()?.sorted() ?: listOf(30)
            
        } catch (e: Exception) {
            Log.e(TAG, "Error initializing manual controls", e)
        }
    }
    
    fun updateCameraSettings(newSettings: CameraSettings) {
        _cameraSettings.value = newSettings
        applySettingsToCamera()
    }
    
    private fun applySettingsToCamera() {
        val settings = _cameraSettings.value
        
        try {
            // Apply CameraX settings
            camera?.cameraControl?.let { control ->
                // Zoom
                control.setZoomRatio(settings.zoomLevel)
                
                // Torch
                control.enableTorch(settings.torchEnabled)
                
                // Focus mode
                when (settings.focusMode) {
                    FocusMode.AUTO -> control.startFocusAndMetering(
                        FocusMeteringAction.Builder(
                            MeteringPoint.Factory.createPointFactory(1.0f, 1.0f).createPoint(0.5f, 0.5f)
                        ).build()
                    )
                    FocusMode.MANUAL -> settings.manualFocusDistance?.let { distance ->
                        // Manual focus would require Camera2 API
                        applyManualFocus(distance)
                    }
                    else -> {
                        // Other focus modes would be handled via Camera2
                    }
                }
                
                // Exposure compensation
                control.setExposureCompensationIndex(settings.exposureCompensation)
            }
            
            // Apply Camera2 manual controls if supported
            if (manualControlsSupported) {
                applyManualControls(settings)
            }
            
        } catch (e: Exception) {
            Log.e(TAG, "Error applying camera settings", e)
        }
    }
    
    @SuppressLint("MissingPermission")
    private fun applyManualControls(settings: CameraSettings) {
        val cameraId = _currentCameraId.value ?: return
        
        try {
            cameraManager?.openCamera(cameraId, object : CameraDevice.StateCallback() {
                override fun onOpened(camera: CameraDevice) {
                    cameraDevice = camera
                    createManualCaptureSession(settings)
                }
                
                override fun onDisconnected(camera: CameraDevice) {
                    camera.close()
                    cameraDevice = null
                }
                
                override fun onError(camera: CameraDevice, error: Int) {
                    camera.close()
                    cameraDevice = null
                    Log.e(TAG, "Camera error: $error")
                }
            }, backgroundHandler)
            
        } catch (e: Exception) {
            Log.e(TAG, "Error applying manual controls", e)
        }
    }
    
    private fun createManualCaptureSession(settings: CameraSettings) {
        val device = cameraDevice ?: return
        
        try {
            val captureRequestBuilder = device.createCaptureRequest(CameraDevice.TEMPLATE_PREVIEW)
            
            // Apply manual settings
            when (settings.focusMode) {
                FocusMode.MANUAL -> {
                    captureRequestBuilder.set(CaptureRequest.CONTROL_AF_MODE, CaptureRequest.CONTROL_AF_MODE_OFF)
                    settings.manualFocusDistance?.let { distance ->
                        val minFocusDistance = cameraCharacteristics?.get(CameraCharacteristics.LENS_INFO_MINIMUM_FOCUS_DISTANCE) ?: 0f
                        captureRequestBuilder.set(CaptureRequest.LENS_FOCUS_DISTANCE, distance * minFocusDistance)
                    }
                }
                FocusMode.CONTINUOUS_VIDEO -> {
                    captureRequestBuilder.set(CaptureRequest.CONTROL_AF_MODE, CaptureRequest.CONTROL_AF_MODE_CONTINUOUS_VIDEO)
                }
                FocusMode.CONTINUOUS_PICTURE -> {
                    captureRequestBuilder.set(CaptureRequest.CONTROL_AF_MODE, CaptureRequest.CONTROL_AF_MODE_CONTINUOUS_PICTURE)
                }
                else -> {
                    captureRequestBuilder.set(CaptureRequest.CONTROL_AF_MODE, CaptureRequest.CONTROL_AF_MODE_AUTO)
                }
            }
            
            // Exposure mode
            when (settings.exposureMode) {
                ExposureMode.MANUAL -> {
                    captureRequestBuilder.set(CaptureRequest.CONTROL_AE_MODE, CaptureRequest.CONTROL_AE_MODE_OFF)
                    settings.manualExposureTime?.let { exposureTime ->
                        captureRequestBuilder.set(CaptureRequest.SENSOR_EXPOSURE_TIME, exposureTime)
                    }
                    settings.manualIsoValue?.let { iso ->
                        captureRequestBuilder.set(CaptureRequest.SENSOR_SENSITIVITY, iso)
                    }
                }
                else -> {
                    captureRequestBuilder.set(CaptureRequest.CONTROL_AE_MODE, CaptureRequest.CONTROL_AE_MODE_ON)
                }
            }
            
            // White balance
            when (settings.whiteBalanceMode) {
                WhiteBalanceMode.MANUAL -> {
                    captureRequestBuilder.set(CaptureRequest.CONTROL_AWB_MODE, CaptureRequest.CONTROL_AWB_MODE_OFF)
                }
                WhiteBalanceMode.DAYLIGHT -> {
                    captureRequestBuilder.set(CaptureRequest.CONTROL_AWB_MODE, CaptureRequest.CONTROL_AWB_MODE_DAYLIGHT)
                }
                WhiteBalanceMode.CLOUDY -> {
                    captureRequestBuilder.set(CaptureRequest.CONTROL_AWB_MODE, CaptureRequest.CONTROL_AWB_MODE_CLOUDY_DAYLIGHT)
                }
                WhiteBalanceMode.SHADE -> {
                    captureRequestBuilder.set(CaptureRequest.CONTROL_AWB_MODE, CaptureRequest.CONTROL_AWB_MODE_SHADE)
                }
                WhiteBalanceMode.TUNGSTEN -> {
                    captureRequestBuilder.set(CaptureRequest.CONTROL_AWB_MODE, CaptureRequest.CONTROL_AWB_MODE_INCANDESCENT)
                }
                WhiteBalanceMode.FLUORESCENT -> {
                    captureRequestBuilder.set(CaptureRequest.CONTROL_AWB_MODE, CaptureRequest.CONTROL_AWB_MODE_FLUORESCENT)
                }
                else -> {
                    captureRequestBuilder.set(CaptureRequest.CONTROL_AWB_MODE, CaptureRequest.CONTROL_AWB_MODE_AUTO)
                }
            }
            
            // Scene mode
            when (settings.sceneMode) {
                SceneMode.PORTRAIT -> captureRequestBuilder.set(CaptureRequest.CONTROL_SCENE_MODE, CaptureRequest.CONTROL_SCENE_MODE_PORTRAIT)
                SceneMode.LANDSCAPE -> captureRequestBuilder.set(CaptureRequest.CONTROL_SCENE_MODE, CaptureRequest.CONTROL_SCENE_MODE_LANDSCAPE)
                SceneMode.NIGHT -> captureRequestBuilder.set(CaptureRequest.CONTROL_SCENE_MODE, CaptureRequest.CONTROL_SCENE_MODE_NIGHT)
                SceneMode.SPORTS -> captureRequestBuilder.set(CaptureRequest.CONTROL_SCENE_MODE, CaptureRequest.CONTROL_SCENE_MODE_SPORTS)
                SceneMode.SUNSET -> captureRequestBuilder.set(CaptureRequest.CONTROL_SCENE_MODE, CaptureRequest.CONTROL_SCENE_MODE_SUNSET)
                SceneMode.FIREWORKS -> captureRequestBuilder.set(CaptureRequest.CONTROL_SCENE_MODE, CaptureRequest.CONTROL_SCENE_MODE_FIREWORKS)
                SceneMode.SNOW -> captureRequestBuilder.set(CaptureRequest.CONTROL_SCENE_MODE, CaptureRequest.CONTROL_SCENE_MODE_SNOW)
                SceneMode.BEACH -> captureRequestBuilder.set(CaptureRequest.CONTROL_SCENE_MODE, CaptureRequest.CONTROL_SCENE_MODE_BEACH)
                SceneMode.CANDLELIGHT -> captureRequestBuilder.set(CaptureRequest.CONTROL_SCENE_MODE, CaptureRequest.CONTROL_SCENE_MODE_CANDLELIGHT)
                SceneMode.PARTY -> captureRequestBuilder.set(CaptureRequest.CONTROL_SCENE_MODE, CaptureRequest.CONTROL_SCENE_MODE_PARTY)
                SceneMode.THEATRE -> captureRequestBuilder.set(CaptureRequest.CONTROL_SCENE_MODE, CaptureRequest.CONTROL_SCENE_MODE_THEATRE)
                SceneMode.ACTION -> captureRequestBuilder.set(CaptureRequest.CONTROL_SCENE_MODE, CaptureRequest.CONTROL_SCENE_MODE_ACTION)
                SceneMode.BARCODE -> captureRequestBuilder.set(CaptureRequest.CONTROL_SCENE_MODE, CaptureRequest.CONTROL_SCENE_MODE_BARCODE)
                else -> captureRequestBuilder.set(CaptureRequest.CONTROL_SCENE_MODE, CaptureRequest.CONTROL_SCENE_MODE_DISABLED)
            }
            
            // Image stabilization
            if (settings.opticalStabilization) {
                captureRequestBuilder.set(CaptureRequest.LENS_OPTICAL_STABILIZATION_MODE, CaptureRequest.LENS_OPTICAL_STABILIZATION_MODE_ON)
            }
            
            if (settings.digitalStabilization) {
                captureRequestBuilder.set(CaptureRequest.CONTROL_VIDEO_STABILIZATION_MODE, CaptureRequest.CONTROL_VIDEO_STABILIZATION_MODE_ON)
            }
            
            // Noise reduction
            when (settings.noiseReduction) {
                NoiseReduction.OFF -> captureRequestBuilder.set(CaptureRequest.NOISE_REDUCTION_MODE, CaptureRequest.NOISE_REDUCTION_MODE_OFF)
                NoiseReduction.HIGH_QUALITY -> captureRequestBuilder.set(CaptureRequest.NOISE_REDUCTION_MODE, CaptureRequest.NOISE_REDUCTION_MODE_HIGH_QUALITY)
                NoiseReduction.FAST -> captureRequestBuilder.set(CaptureRequest.NOISE_REDUCTION_MODE, CaptureRequest.NOISE_REDUCTION_MODE_FAST)
                NoiseReduction.ZERO_SHUTTER_LAG -> captureRequestBuilder.set(CaptureRequest.NOISE_REDUCTION_MODE, CaptureRequest.NOISE_REDUCTION_MODE_ZERO_SHUTTER_LAG)
                else -> captureRequestBuilder.set(CaptureRequest.NOISE_REDUCTION_MODE, CaptureRequest.NOISE_REDUCTION_MODE_FAST)
            }
            
            // Edge enhancement
            when (settings.edgeEnhancement) {
                EdgeEnhancement.OFF -> captureRequestBuilder.set(CaptureRequest.EDGE_MODE, CaptureRequest.EDGE_MODE_OFF)
                EdgeEnhancement.HIGH_QUALITY -> captureRequestBuilder.set(CaptureRequest.EDGE_MODE, CaptureRequest.EDGE_MODE_HIGH_QUALITY)
                EdgeEnhancement.FAST -> captureRequestBuilder.set(CaptureRequest.EDGE_MODE, CaptureRequest.EDGE_MODE_FAST)
                EdgeEnhancement.ZERO_SHUTTER_LAG -> captureRequestBuilder.set(CaptureRequest.EDGE_MODE, CaptureRequest.EDGE_MODE_ZERO_SHUTTER_LAG)
                else -> captureRequestBuilder.set(CaptureRequest.EDGE_MODE, CaptureRequest.EDGE_MODE_FAST)
            }
            
            // Hot pixel correction
            captureRequestBuilder.set(CaptureRequest.HOT_PIXEL_MODE, 
                if (settings.hotPixelCorrection) CaptureRequest.HOT_PIXEL_MODE_HIGH_QUALITY 
                else CaptureRequest.HOT_PIXEL_MODE_OFF
            )
            
        } catch (e: Exception) {
            Log.e(TAG, "Error creating manual capture session", e)
        }
    }
    
    private fun applyManualFocus(focusDistance: Float) {
        // Implementation for manual focus using Camera2 API
        // This would be called when manual focus is enabled
    }
    
    fun switchCamera() {
        val cameras = _availableCameras.value
        val currentIndex = cameras.indexOfFirst { it.id == _currentCameraId.value }
        val nextIndex = (currentIndex + 1) % cameras.size
        _currentCameraId.value = cameras[nextIndex].id
        
        // Reinitialize camera with new camera ID
        // This would trigger a camera restart
    }
    
    fun getSupportedResolutions(): List<Resolution> = supportedResolutions
    
    fun getSupportedFrameRates(): List<Int> = supportedFrameRates
    
    fun isManualControlSupported(): Boolean = manualControlsSupported
    
    fun release() {
        cameraProvider?.unbindAll()
        cameraDevice?.close()
        cameraDevice = null
        captureSession?.close()
        captureSession = null
        stopBackgroundThread()
        cameraExecutor.shutdown()
    }
}

data class CameraInfo(
    val id: String,
    val facing: CameraFacing,
    val displayName: String
)

enum class CameraFacing {
    FRONT,
    BACK,
    EXTERNAL
} 