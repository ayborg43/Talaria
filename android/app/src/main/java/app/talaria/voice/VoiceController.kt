package app.talaria.voice

import android.Manifest
import android.annotation.SuppressLint
import android.app.Application
import android.content.pm.PackageManager
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.MediaRecorder
import android.media.audiofx.AcousticEchoCanceler
import android.media.audiofx.NoiseSuppressor
import android.os.Bundle
import android.speech.tts.TextToSpeech
import android.speech.tts.UtteranceProgressListener
import androidx.core.content.ContextCompat
import app.talaria.BuildConfig
import app.talaria.data.Repo
import app.talaria.data.VoiceDto
import app.talaria.data.TextBody
import app.talaria.data.AppJson
import app.talaria.ui.components.plainText
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.util.ArrayDeque
import java.util.Locale
import java.util.concurrent.atomic.AtomicInteger
import kotlin.math.sqrt

enum class Phase { Off, Listening, Hearing, Transcribing, Thinking, Speaking }

/**
 * Talking with Talaria. One tap starts a conversation: it listens, notices when you stop, turns your voice into text
 * with the speech service set up on your server, sends it, and reads the reply aloud; then it listens again. Speaking
 * over a reply interrupts it. The microphone is open only while this is on, and a tap, leaving the app, or a long
 * silence turns it off. What you say is sent as a chat message like any other: it can never answer an approval.
 */
class VoiceController(private val app: Application, private val repo: Repo) {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private val _phase = MutableStateFlow(Phase.Off)
    val phase: StateFlow<Phase> = _phase.asStateFlow()
    private val _level = MutableStateFlow(0f)
    val level: StateFlow<Float> = _level.asStateFlow()
    private val _note = MutableStateFlow("")
    /** What was heard, or what went wrong, for the panel to show. */
    val note: StateFlow<String> = _note.asStateFlow()
    /** Something to tell the person once the conversation is over or could not start. */
    data class Notice(val text: String, val isError: Boolean = true)
    private val _notice = MutableStateFlow<Notice?>(null)
    val notice: StateFlow<Notice?> = _notice.asStateFlow()
    fun clearNotice() { _notice.value = null }
    private val _needsMic = MutableStateFlow(false)
    val needsMic: StateFlow<Boolean> = _needsMic.asStateFlow()

    /** When set, the next thing heard goes here (into the message box) instead of being sent. */
    @Volatile private var dictation: ((String) -> Unit)? = null

    private var job: Job? = null
    private var watcher: Job? = null
    private var tts: TextToSpeech? = null
    @Volatile private var ttsReady = false
    private val ttsPending = AtomicInteger(0)
    private val spoken = HashSet<Long>()
    @Volatile private var awaitingSince = 0L
    private var lastActivity = 0L

    fun hasPermission() = ContextCompat.checkSelfPermission(app, Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED
    fun micGranted() { _needsMic.value = false; startConversation() }
    fun micDenied() { _needsMic.value = false; _notice.value = Notice("The microphone is blocked. Allow it for Talaria in the phone's settings.") }

    fun toggleConversation() { if (_phase.value == Phase.Off) startConversation() else stop() }

    fun startDictation(into: (String) -> Unit) { dictation = into; if (_phase.value == Phase.Off) startConversation() }

    fun startConversation() {
        if (_phase.value != Phase.Off) return
        if (!hasPermission()) { _needsMic.value = true; return }
        _note.value = ""; _notice.value = null
        scope.launch {
            val v = runCatching { repo.api.getJson<VoiceDto>("/api/voice") }.getOrNull()
            if (v == null || !v.available) {
                _notice.value = Notice("Voice is not set up on your server yet. Add a speech service in Settings, Voice.")
                dictation = null
                return@launch
            }
            begin(v.max_seconds)
        }
    }

    private fun begin(maxSeconds: Int) {
        spoken.clear()
        repo.messages.value.forEach { spoken += it.id }
        awaitingSince = 0
        lastActivity = System.currentTimeMillis()
        if (repo.store.speakReplies) initTts()
        _phase.value = Phase.Listening
        job = scope.launch { listen(maxSeconds) }
        watcher = scope.launch { watch() }
    }

    fun stop() {
        job?.cancel(); watcher?.cancel(); job = null; watcher = null
        tts?.stop(); ttsPending.set(0)
        dictation = null
        _level.value = 0f
        _note.value = ""
        _phase.value = Phase.Off
    }

    fun release() { stop(); tts?.shutdown(); tts = null; ttsReady = false }

    // ---- listening ----

    @SuppressLint("MissingPermission")
    private suspend fun listen(maxSeconds: Int) = withContext(Dispatchers.IO) {
        val frame = Wav.RATE / 50 // 20 ms
        val minBuf = AudioRecord.getMinBufferSize(Wav.RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT)
        val rec = try {
            AudioRecord(MediaRecorder.AudioSource.VOICE_COMMUNICATION, Wav.RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT, maxOf(minBuf, frame * 2 * 16))
        } catch (e: Exception) { _notice.value = Notice("The microphone could not be opened."); _phase.value = Phase.Off; return@withContext }
        if (rec.state != AudioRecord.STATE_INITIALIZED) { rec.release(); _notice.value = Notice("The microphone could not be opened."); _phase.value = Phase.Off; return@withContext }
        // Echo cancelling and noise suppression keep the reply it is speaking out of what it hears.
        val aec = runCatching { if (AcousticEchoCanceler.isAvailable()) AcousticEchoCanceler.create(rec.audioSessionId)?.also { it.enabled = true } else null }.getOrNull()
        val ns = runCatching { if (NoiseSuppressor.isAvailable()) NoiseSuppressor.create(rec.audioSessionId)?.also { it.enabled = true } else null }.getOrNull()
        rec.startRecording()
        val buf = ShortArray(frame)
        val pre = ArrayDeque<ShortArray>()
        val speech = Wav.Samples()
        val vad = Vad()
        val maxFrames = maxOf(10, minOf(maxSeconds, 90)) * 50
        try {
            while (isActive) {
                var n = 0
                while (n < frame) { val r = rec.read(buf, n, frame - n); if (r <= 0) break; n += r }
                if (n < frame) continue
                var sum = 0.0
                for (i in 0 until frame) { val v = buf[i] / 32768.0; sum += v * v }
                val rms = sqrt(sum / frame).toFloat()
                _level.value = (_level.value * 0.6f + (rms * 9f).coerceIn(0f, 1f) * 0.4f)
                val phase = _phase.value
                when {
                    phase == Phase.Listening || phase == Phase.Speaking || phase == Phase.Hearing -> {
                        if (phase != Phase.Hearing) { pre.addLast(buf.copyOf()); while (pre.size > 20) pre.removeFirst() } else speech.add(buf)
                        when (val e = vad.feed(rms, phoneIsSpeaking = phase == Phase.Speaking, maxFrames = maxFrames)) {
                            Vad.Event.Started -> {
                                if (phase == Phase.Speaking) { tts?.stop(); ttsPending.set(0) }
                                _phase.value = Phase.Hearing
                                speech.clear(); pre.forEach { speech.add(it) }; pre.clear()
                                lastActivity = System.currentTimeMillis()
                            }
                            is Vad.Event.Ended -> {
                                val samples = speech.toArray(); speech.clear()
                                if (!e.enough) _phase.value = Phase.Listening
                                else { _phase.value = Phase.Transcribing; scope.launch { transcribeAndSend(samples) } }
                            }
                            Vad.Event.None -> if (phase == Phase.Listening && System.currentTimeMillis() - lastActivity > IDLE_MS) {
                                _notice.value = Notice("Stopped listening because it was quiet.", isError = false)
                                withContext(Dispatchers.Main) { stop() }
                            }
                        }
                    }
                    else -> { pre.clear(); vad.reset() } // transcribing or waiting for the reply: what is heard now is not for us
                }
            }
        } finally {
            runCatching { rec.stop() }; rec.release(); aec?.release(); ns?.release()
        }
    }

    /** Sends a recording as if it had just been heard. For tests, where no one is speaking into a microphone. */
    fun heardForTest(samples: ShortArray) { if (_phase.value == Phase.Off) { begin(60) }; _phase.value = Phase.Transcribing; scope.launch { transcribeAndSend(samples) } }

    private suspend fun transcribeAndSend(samples: ShortArray) {
        try {
            val body = repo.api.postBytes("/api/transcribe", Wav.encode(samples), "audio/wav")
            val text = AppJson.decodeFromString<TextBody>(body).text.trim()
            if (text.isEmpty()) { _phase.value = Phase.Listening; return }
            _note.value = text
            lastActivity = System.currentTimeMillis()
            val sink = dictation
            if (sink != null) { dictation = null; sink(text); stop(); return }
            awaitingSince = System.currentTimeMillis() // before sending: a quick reply must find us already waiting for it
            _phase.value = Phase.Thinking
            try { repo.send(text) } catch (e: Exception) { awaitingSince = 0; throw e }
        } catch (e: Exception) {
            _notice.value = Notice(e.message ?: "That could not be turned into text.")
            if (_phase.value != Phase.Off) _phase.value = Phase.Listening
        }
    }

    // ---- the reply ----

    /** Reads new replies aloud as they arrive, and goes back to listening when the agent is done. */
    private suspend fun watch() {
        while (scope.isActive) {
            delay(120)
            val phase = _phase.value
            if (phase == Phase.Off) return
            val waiting = awaitingSince > 0
            for (m in repo.messages.value) {
                if (m.role == "assistant" && m.text.isNotBlank() && spoken.add(m.id) && waiting) speak(m.text)
            }
            if (BuildConfig.DEBUG) android.util.Log.d("TalVoice", "phase=$phase waiting=$waiting running=${repo.running.value} tts=${ttsPending.get()}")
            if (awaitingSince > 0 && (phase == Phase.Thinking || phase == Phase.Speaking)) {
                val quiet = !repo.running.value && ttsPending.get() == 0
                val approval = repo.pendingApprovals.isNotEmpty()
                if (quiet && !approval && System.currentTimeMillis() - awaitingSince > 1500) {
                    awaitingSince = 0; lastActivity = System.currentTimeMillis()
                    _phase.value = Phase.Listening
                }
            }
        }
    }

    private fun initTts() {
        if (tts != null) return
        tts = TextToSpeech(app) { status ->
            if (status == TextToSpeech.SUCCESS) {
                tts?.language = Locale.getDefault().takeIf { tts?.isLanguageAvailable(it) ?: -2 >= 0 } ?: Locale.US
                tts?.setOnUtteranceProgressListener(object : UtteranceProgressListener() {
                    override fun onStart(id: String?) { if (_phase.value == Phase.Thinking) _phase.value = Phase.Speaking }
                    override fun onDone(id: String?) { ttsPending.updateAndGet { maxOf(0, it - 1) } }
                    @Deprecated("Deprecated in Java") override fun onError(id: String?) { ttsPending.updateAndGet { maxOf(0, it - 1) } }
                })
                ttsReady = true
            }
        }
    }

    private fun speak(markdown: String) {
        val engine = tts ?: return
        if (!ttsReady) return
        var text = plainText(markdown)
        var cut = false
        if (text.length > 700) { text = text.take(700).substringBeforeLast(' ') + "."; cut = true }
        val parts = text.split(Regex("(?<=[.!?])\\s+")).flatMap { it.chunked(220) }.filter { it.isNotBlank() }.toMutableList()
        if (cut) parts += "The rest is on the screen."
        for ((i, p) in parts.withIndex()) {
            ttsPending.incrementAndGet()
            if (engine.speak(p, TextToSpeech.QUEUE_ADD, Bundle(), "r${System.nanoTime()}-$i") != TextToSpeech.SUCCESS) ttsPending.updateAndGet { maxOf(0, it - 1) }
        }
        if (_phase.value == Phase.Thinking) _phase.value = Phase.Speaking
    }

    private companion object { const val IDLE_MS = 90_000L }
}
