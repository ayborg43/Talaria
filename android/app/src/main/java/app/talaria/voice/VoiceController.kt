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
import android.content.Intent
import android.os.Bundle
import android.speech.RecognitionListener
import android.speech.RecognizerIntent
import android.speech.SpeechRecognizer
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
import kotlinx.coroutines.flow.collect
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
 * Talking with Talaria. One tap starts a conversation: it listens, notices when you stop, turns your voice into text,
 * sends it, and reads the reply aloud; then it listens again. The microphone is open only while this is on, and a tap,
 * leaving the app, or a long silence turns it off. What you say is sent as a chat message like any other: it can never
 * answer an approval.
 *
 * Two engines can turn the voice into text. The phone's own recognizer (the default, kept on the phone unless the
 * person allows otherwise) hears you and shows words as you speak; it is not listening while a reply is being read,
 * so speaking over a reply does not interrupt it. The speech service on the person's server listens with this app's
 * own silence detection and does allow speaking over a reply.
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

    // ---- the phone's own recognizer (all of this runs on the main thread, which SpeechRecognizer requires) ----
    @Volatile private var engine = Engine.Server
    private val main = CoroutineScope(SupervisorJob() + Dispatchers.Main)
    private var phaseJob: Job? = null
    private var recognizer: SpeechRecognizer? = null
    private var recognizing = false
    private var retried = false
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
            val server = runCatching { repo.api.getJson<VoiceDto>("/api/voice") }.getOrNull()
            val localOnly = repo.store.speechLocalOnly
            val has = PhoneSpeech.exists(app)
            val support = Engines.support(has.first, has.second, localOnly)
            val why = Engines.why(has.first, has.second, localOnly)
            when (val pick = Engines.choose(repo.store.speechEngine, server?.available == true, support, why)) {
                is Pick.Use -> { engine = pick.engine; begin(server?.max_seconds ?: 60) }
                is Pick.Unavailable -> { _notice.value = Notice(pick.message); dictation = null }
            }
        }
    }

    private fun begin(maxSeconds: Int) {
        spoken.clear()
        repo.messages.value.forEach { spoken += it.id }
        awaitingSince = 0
        lastActivity = System.currentTimeMillis()
        if (repo.store.speakReplies) initTts()
        _phase.value = Phase.Listening
        if (engine == Engine.Phone) phaseJob = main.launch { _phase.collect { followPhase(it) } }
        else job = scope.launch { listen(maxSeconds) }
        watcher = scope.launch { watch() }
    }

    fun stop() {
        job?.cancel(); watcher?.cancel(); job = null; watcher = null
        phaseJob?.cancel(); phaseJob = null
        main.launch { recognizer?.runCatching { cancel(); destroy() }; recognizer = null; recognizing = false }
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
            handleText(text)
        } catch (e: Exception) {
            _notice.value = Notice(e.message ?: "That could not be turned into text.")
            if (_phase.value != Phase.Off) _phase.value = Phase.Listening
        }
    }

    /** What was heard, from either engine: into the message box if that is what was asked, else sent to the agent. */
    private suspend fun handleText(text: String) {
        _note.value = text
        lastActivity = System.currentTimeMillis()
        val sink = dictation
        if (sink != null) { dictation = null; sink(text); stop(); return }
        awaitingSince = System.currentTimeMillis() // before sending: a quick reply must find us already waiting for it
        _phase.value = Phase.Thinking
        try { repo.send(text) } catch (e: Exception) { awaitingSince = 0; throw e }
    }

    /** Sends words as if they had just been heard. For tests, where no one is speaking into a microphone. */
    fun heardTextForTest(text: String) {
        if (_phase.value == Phase.Off) begin(60)
        scope.launch { runCatching { handleText(text) }.onFailure { android.util.Log.w("TalVoice", "heardTextForTest failed", it) } }
    }

    // ---- listening with the phone's own recognizer ----

    /** Starts and stops the recognizer as the conversation moves: it listens when it is our turn, and is quiet while a reply is read. */
    private fun followPhase(p: Phase) {
        when (p) {
            Phase.Listening -> startRecognizing()
            Phase.Hearing, Phase.Transcribing -> {} // it is working on what it heard
            else -> if (recognizing) { recognizer?.cancel(); recognizing = false }
        }
    }

    private fun recognizerIntent(): Intent = Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH)
        .putExtra(RecognizerIntent.EXTRA_LANGUAGE_MODEL, RecognizerIntent.LANGUAGE_MODEL_FREE_FORM)
        .putExtra(RecognizerIntent.EXTRA_LANGUAGE, Locale.getDefault().toLanguageTag())
        .putExtra(RecognizerIntent.EXTRA_PARTIAL_RESULTS, true)
        .putExtra(RecognizerIntent.EXTRA_MAX_RESULTS, 1)
        .putExtra(RecognizerIntent.EXTRA_PREFER_OFFLINE, repo.store.speechLocalOnly)
        .putExtra(RecognizerIntent.EXTRA_SPEECH_INPUT_COMPLETE_SILENCE_LENGTH_MILLIS, 1400L)
        .putExtra(RecognizerIntent.EXTRA_CALLING_PACKAGE, app.packageName)

    private fun startRecognizing() {
        if (recognizing || engine != Engine.Phone || _phase.value != Phase.Listening) return
        val r = recognizer ?: PhoneSpeech.create(app, repo.store.speechLocalOnly)?.also { recognizer = it; it.setRecognitionListener(listener) }
        if (r == null) { _notice.value = Notice("The phone's speech recognition could not be started."); stop(); return }
        recognizing = true
        if (BuildConfig.DEBUG) android.util.Log.d("TalVoice", "recognizer: start listening (${engine})")
        try { r.startListening(recognizerIntent()) } catch (e: Exception) { recognizing = false; _notice.value = Notice("The phone's speech recognition could not be started."); stop() }
    }

    private fun listenAgainSoon(delayMs: Long = 150) {
        main.launch { delay(delayMs); if (_phase.value == Phase.Listening) startRecognizing() }
    }

    private val listener = object : RecognitionListener {
        override fun onReadyForSpeech(params: Bundle?) { retried = false }
        override fun onBeginningOfSpeech() { if (_phase.value == Phase.Listening) { _phase.value = Phase.Hearing; lastActivity = System.currentTimeMillis() } }
        override fun onRmsChanged(rmsdB: Float) { _level.value = _level.value * 0.6f + ((rmsdB + 2f) / 12f).coerceIn(0f, 1f) * 0.4f }
        override fun onBufferReceived(buffer: ByteArray?) {}
        override fun onEndOfSpeech() { if (_phase.value == Phase.Hearing) _phase.value = Phase.Transcribing }
        override fun onPartialResults(partialResults: Bundle?) {
            partialResults?.getStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION)?.firstOrNull()?.takeIf { it.isNotBlank() }?.let { _note.value = it }
        }
        override fun onEvent(eventType: Int, params: Bundle?) {}
        override fun onResults(results: Bundle?) {
            if (BuildConfig.DEBUG) android.util.Log.d("TalVoice", "recognizer: results")
            recognizing = false
            val text = results?.getStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION)?.firstOrNull()?.trim().orEmpty()
            if (text.isEmpty()) { _phase.value = Phase.Listening; listenAgainSoon(); return }
            scope.launch {
                try { handleText(text) }
                catch (e: Exception) { _notice.value = Notice(e.message ?: "That could not be sent."); if (_phase.value != Phase.Off) { _phase.value = Phase.Listening; main.launch { startRecognizing() } } }
            }
        }
        override fun onError(error: Int) {
            if (BuildConfig.DEBUG) android.util.Log.d("TalVoice", "recognizer: error $error")
            recognizing = false
            _level.value = 0f
            if (_phase.value == Phase.Off) return
            when (val a = Engines.onError(error)) {
                ErrorAction.ListenAgain -> {
                    if (System.currentTimeMillis() - lastActivity > IDLE_MS) { _notice.value = Notice("Stopped listening because it was quiet.", isError = false); stop(); return }
                    _phase.value = Phase.Listening; _note.value = ""; listenAgainSoon(250)
                }
                ErrorAction.RetryOnce -> {
                    if (retried) { _notice.value = Notice("The phone's speech recognition keeps failing. Try again in a moment."); stop(); return }
                    retried = true
                    recognizer?.destroy(); recognizer = null
                    _phase.value = Phase.Listening; listenAgainSoon(600)
                }
                ErrorAction.NeedsLanguagePack -> {
                    val started = PhoneSpeech.fetchLanguagePack(app, recognizer, recognizerIntent())
                    _notice.value = Notice(
                        if (started) "Talaria needs the offline speech pack for your language. Android is downloading it: try again in a minute."
                        else "This phone has no offline speech pack for your language. Download one in the phone's speech settings, then try again.",
                        isError = !started,
                    )
                    stop()
                }
                is ErrorAction.Stop -> { _notice.value = Notice(a.message); stop() }
            }
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
