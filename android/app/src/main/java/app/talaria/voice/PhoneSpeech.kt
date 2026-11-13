package app.talaria.voice

import android.content.Context
import android.content.Intent
import android.os.Build
import android.speech.RecognitionSupport
import android.speech.RecognitionSupportCallback
import android.speech.SpeechRecognizer
import androidx.core.content.ContextCompat

/** The Android side of the phone's own speech recognizer: what the phone has, and how to make one. */
object PhoneSpeech {
    /** Whether the phone has a recognizer that runs on the phone, and whether it has one that may use an online service. */
    fun exists(ctx: Context): Pair<Boolean, Boolean> {
        val onDevice = Build.VERSION.SDK_INT >= 31 && SpeechRecognizer.isOnDeviceRecognitionAvailable(ctx)
        val online = SpeechRecognizer.isRecognitionAvailable(ctx)
        return onDevice to online
    }

    /**
     * A recognizer. With [localOnly] it is the on-device one and nothing else: if the phone has none, there is none to
     * return, rather than one that could send the audio to an online service.
     */
    fun create(ctx: Context, localOnly: Boolean): SpeechRecognizer? {
        val (onDevice, online) = exists(ctx)
        return when {
            onDevice && Build.VERSION.SDK_INT >= 31 -> SpeechRecognizer.createOnDeviceSpeechRecognizer(ctx)
            online && !localOnly -> SpeechRecognizer.createSpeechRecognizer(ctx)
            else -> null
        }
    }

    /** Asks Android to download the offline language pack for what was asked for. False when this Android cannot do that from an app. */
    fun fetchLanguagePack(ctx: Context, recognizer: SpeechRecognizer?, intent: Intent): Boolean {
        if (Build.VERSION.SDK_INT < 33 || recognizer == null) return false
        return runCatching {
            recognizer.checkRecognitionSupport(intent, ContextCompat.getMainExecutor(ctx), object : RecognitionSupportCallback {
                override fun onSupportResult(recognitionSupport: RecognitionSupport) {
                    val wanted = intent.getStringExtra(android.speech.RecognizerIntent.EXTRA_LANGUAGE)
                    if (recognitionSupport.installedOnDeviceLanguages.isEmpty() || wanted !in recognitionSupport.installedOnDeviceLanguages) runCatching { recognizer.triggerModelDownload(intent) }
                }
                override fun onError(error: Int) { runCatching { recognizer.triggerModelDownload(intent) } }
            })
            true
        }.getOrDefault(false)
    }
}
