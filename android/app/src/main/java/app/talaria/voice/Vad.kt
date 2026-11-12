package app.talaria.voice

import kotlin.math.max

/**
 * Decides, frame by frame, when someone starts and stops talking. A frame is 20 ms of audio, given as its loudness
 * (RMS, 0 to 1). The bar for "this is speech" follows the room's background level, and is much higher while the
 * phone itself is speaking, so its own voice leaking into the microphone does not count as being interrupted.
 */
class Vad {
    sealed interface Event {
        data object None : Event
        data object Started : Event
        /** The person stopped. [enough] is false for a cough or a click, which is not worth sending. */
        data class Ended(val enough: Boolean) : Event
    }

    var hearing = false
        private set
    var noise = 0.008f
        private set
    private var voiced = 0
    private var silence = 0
    private var frames = 0
    private var voicedInUtterance = 0
    private val calibration = ArrayList<Float>(CALIBRATION_FRAMES)

    fun reset() { hearing = false; voiced = 0; silence = 0; frames = 0; voicedInUtterance = 0 }

    fun threshold(phoneIsSpeaking: Boolean): Float = if (phoneIsSpeaking) max(0.07f, noise * 7f) else max(0.02f, noise * 3.2f)

    fun feed(rms: Float, phoneIsSpeaking: Boolean, maxFrames: Int): Event {
        // The first moments are spent learning how loud the room is, so that a fan or traffic is not taken for speech.
        // The quiet end of what is heard is used, in case the person has already started to talk.
        if (calibration.size < CALIBRATION_FRAMES) {
            calibration.add(rms)
            if (calibration.size == CALIBRATION_FRAMES) noise = max(0.004f, calibration.sorted()[CALIBRATION_FRAMES / 10])
            return Event.None
        }
        val isVoice = rms > threshold(phoneIsSpeaking)
        if (!hearing) {
            if (isVoice) voiced++ else { voiced = 0; noise = noise * 0.96f + rms * 0.04f }
            if (voiced >= if (phoneIsSpeaking) INTERRUPT_FRAMES else START_FRAMES) {
                hearing = true; silence = 0; frames = 0; voicedInUtterance = voiced; voiced = 0
                return Event.Started
            }
            return Event.None
        }
        frames++
        if (isVoice) { silence = 0; voicedInUtterance++ } else { silence++; noise = noise * 0.99f + rms * 0.01f }
        if (silence >= END_FRAMES || frames >= maxFrames) {
            val enough = voicedInUtterance >= MIN_VOICED_FRAMES
            reset()
            return Event.Ended(enough)
        }
        return Event.None
    }

    companion object {
        const val CALIBRATION_FRAMES = 30 // 600 ms to learn the room
        const val START_FRAMES = 3        // 60 ms of speech to start listening to it
        const val INTERRUPT_FRAMES = 9    // 180 ms of loud speech to interrupt the phone's own voice
        const val END_FRAMES = 55         // 1.1 s of quiet ends a turn
        const val MIN_VOICED_FRAMES = 12  // under about a quarter of a second of speech is not a turn
    }
}
