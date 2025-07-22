package com.frameworks.misthose.models

data class CameraSettings(
    // Basic settings
    val resolution: Resolution = Resolution.HD_720P,
    val frameRate: Int = 30,
    val bitrate: Int = 2500,
    
    // Manual camera controls
    val focusMode: FocusMode = FocusMode.AUTO,
    val exposureMode: ExposureMode = ExposureMode.AUTO,
    val whiteBalanceMode: WhiteBalanceMode = WhiteBalanceMode.AUTO,
    val isoMode: IsoMode = IsoMode.AUTO,
    
    // Manual values (when not in auto mode)
    val manualFocusDistance: Float? = null, // 0.0f to 1.0f
    val manualExposureTime: Long? = null, // nanoseconds
    val manualIsoValue: Int? = null, // ISO value
    val exposureCompensation: Int = 0, // -2 to +2 EV
    
    // Flash and torch
    val flashMode: FlashMode = FlashMode.OFF,
    val torchEnabled: Boolean = false,
    
    // Image stabilization
    val opticalStabilization: Boolean = false,
    val digitalStabilization: Boolean = false,
    
    // Zoom
    val zoomLevel: Float = 1.0f, // 1.0f = no zoom
    
    // Scene and color
    val sceneMode: SceneMode = SceneMode.AUTO,
    val colorEffect: ColorEffect = ColorEffect.NONE,
    
    // Advanced settings
    val noiseReduction: NoiseReduction = NoiseReduction.AUTO,
    val edgeEnhancement: EdgeEnhancement = EdgeEnhancement.AUTO,
    val hotPixelCorrection: Boolean = true,
    
    // Audio settings
    val audioEnabled: Boolean = true,
    val audioGain: Float = 1.0f,
    val audioSource: AudioSource = AudioSource.MIC
)

enum class Resolution(val displayName: String, val width: Int, val height: Int) {
    HD_480P("480p", 854, 480),
    HD_720P("720p", 1280, 720),
    HD_1080P("1080p", 1920, 1080),
    UHD_4K("4K", 3840, 2160);
    
    val aspectRatio: Float get() = width.toFloat() / height.toFloat()
}

enum class FocusMode(val displayName: String) {
    AUTO("Auto Focus"),
    MANUAL("Manual Focus"),
    CONTINUOUS_VIDEO("Continuous Video"),
    CONTINUOUS_PICTURE("Continuous Picture"),
    MACRO("Macro"),
    INFINITY("Infinity"),
    FIXED("Fixed")
}

enum class ExposureMode(val displayName: String) {
    AUTO("Auto Exposure"),
    MANUAL("Manual Exposure"),
    SHUTTER_PRIORITY("Shutter Priority"),
    ISO_PRIORITY("ISO Priority")
}

enum class WhiteBalanceMode(val displayName: String) {
    AUTO("Auto White Balance"),
    MANUAL("Manual"),
    DAYLIGHT("Daylight"),
    CLOUDY("Cloudy"),
    SHADE("Shade"),
    TUNGSTEN("Tungsten"),
    FLUORESCENT("Fluorescent"),
    INCANDESCENT("Incandescent"),
    WARM_FLUORESCENT("Warm Fluorescent")
}

enum class IsoMode(val displayName: String) {
    AUTO("Auto ISO"),
    MANUAL("Manual ISO")
}

enum class FlashMode(val displayName: String) {
    OFF("Off"),
    AUTO("Auto"),
    ON("On"),
    RED_EYE("Red Eye Reduction"),
    TORCH("Torch")
}

enum class SceneMode(val displayName: String) {
    AUTO("Auto"),
    PORTRAIT("Portrait"),
    LANDSCAPE("Landscape"),
    NIGHT("Night"),
    SPORTS("Sports"),
    SUNSET("Sunset"),
    STEADYPHOTO("Steady Photo"),
    FIREWORKS("Fireworks"),
    SNOW("Snow"),
    BEACH("Beach"),
    CANDLELIGHT("Candlelight"),
    PARTY("Party"),
    THEATRE("Theatre"),
    ACTION("Action"),
    BARCODE("Barcode")
}

enum class ColorEffect(val displayName: String) {
    NONE("None"),
    MONO("Monochrome"),
    NEGATIVE("Negative"),
    SOLARIZE("Solarize"),
    SEPIA("Sepia"),
    POSTERIZE("Posterize"),
    WHITEBOARD("Whiteboard"),
    BLACKBOARD("Blackboard"),
    AQUA("Aqua"),
    EMBOSS("Emboss"),
    SKETCH("Sketch"),
    NEON("Neon")
}

enum class NoiseReduction(val displayName: String) {
    OFF("Off"),
    AUTO("Auto"),
    HIGH_QUALITY("High Quality"),
    FAST("Fast"),
    ZERO_SHUTTER_LAG("Zero Shutter Lag")
}

enum class EdgeEnhancement(val displayName: String) {
    OFF("Off"),
    AUTO("Auto"),
    HIGH_QUALITY("High Quality"),
    FAST("Fast"),
    ZERO_SHUTTER_LAG("Zero Shutter Lag")
}

enum class AudioSource(val displayName: String) {
    MIC("Microphone"),
    CAMCORDER("Camcorder"),
    VOICE_RECOGNITION("Voice Recognition"),
    VOICE_COMMUNICATION("Voice Communication"),
    UNPROCESSED("Unprocessed")
} 