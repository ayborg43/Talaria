package app.talaria.voice

/** Who turns the person's voice into text: the phone's own recognizer, or the speech service on the person's server. */
enum class Engine { Phone, Server }

/** What the phone itself offers: a recognizer that runs on the phone, one that may use an online service, or none. */
enum class Support { OnDevice, Online, None }

sealed interface Pick {
    data class Use(val engine: Engine) : Pick
    data class Unavailable(val message: String) : Pick
}

/** What to do when the phone's recognizer reports an error. */
sealed interface ErrorAction {
    /** Nothing was said (or nothing made sense): listen again. */
    data object ListenAgain : ErrorAction
    /** A passing problem: try again once, then stop. */
    data object RetryOnce : ErrorAction
    /** The language pack is missing: ask Android to fetch it. */
    data object NeedsLanguagePack : ErrorAction
    data class Stop(val message: String) : ErrorAction
}

/** The choices behind talking to Talaria, free of Android so they can be tested. */
object Engines {
    const val PHONE = "phone"
    const val SERVER = "server"

    /**
     * Which engine to use. The person's choice wins when it can work; otherwise the other one is used if it can, and
     * only when neither can is there something to tell them. [phoneWhy] says why the phone cannot, in words.
     */
    fun choose(preference: String, serverReady: Boolean, phone: Support, phoneWhy: String): Pick {
        val phoneReady = phone != Support.None
        val wantsServer = preference == SERVER
        return when {
            wantsServer && serverReady -> Pick.Use(Engine.Server)
            wantsServer && phoneReady -> Pick.Use(Engine.Phone)
            !wantsServer && phoneReady -> Pick.Use(Engine.Phone)
            !wantsServer && serverReady -> Pick.Use(Engine.Server)
            else -> Pick.Unavailable(phoneWhy.ifBlank { "Speech recognition is not available on this phone." } + " Your server has no speech service either (Settings, Voice).")
        }
    }

    /** SpeechRecognizer's error codes, as the numbers Android documents. */
    fun onError(code: Int): ErrorAction = when (code) {
        6, 7 -> ErrorAction.ListenAgain // speech timeout, no match
        3, 5, 8, 11 -> ErrorAction.RetryOnce // audio, client, recognizer busy, service disconnected
        12, 13 -> ErrorAction.NeedsLanguagePack // language not supported, language unavailable
        9 -> ErrorAction.Stop("The microphone is blocked. Allow it for Talaria in the phone's settings.")
        1, 2, 4 -> ErrorAction.Stop("The phone could not reach its speech service. Without a connection it needs offline speech: download it in the phone's speech settings.")
        10 -> ErrorAction.Stop("The phone's speech service is busy. Try again in a moment.")
        else -> ErrorAction.Stop("The phone's speech recognition failed (error $code).")
    }

    /** Words for a phone that cannot recognize speech, given what it was asked for. */
    fun why(onDeviceExists: Boolean, onlineExists: Boolean, localOnly: Boolean): String = when {
        onDeviceExists -> ""
        onlineExists && localOnly -> "This phone has no offline speech recognition, and Talaria is set to keep speech on the phone. Download an offline language in the phone's speech settings, or turn that setting off to let Android use its online recognizer."
        onlineExists -> ""
        else -> "This phone has no speech recognition service."
    }

    /** What the rest of the screen should say about the phone's recognizer. */
    fun support(onDeviceExists: Boolean, onlineExists: Boolean, localOnly: Boolean): Support = when {
        onDeviceExists -> Support.OnDevice
        onlineExists && !localOnly -> Support.Online
        else -> Support.None
    }
}
