package app.talaria.assist

import android.app.Service
import android.content.Context
import android.content.Intent
import android.graphics.Bitmap
import android.net.Uri
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.service.voice.VoiceInteractionService
import android.service.voice.VoiceInteractionSession
import android.service.voice.VoiceInteractionSessionService
import android.speech.RecognitionService
import android.speech.SpeechRecognizer
import androidx.core.content.FileProvider
import app.talaria.Handoff
import app.talaria.QuickAction
import app.talaria.TalariaApp
import java.io.File

/**
 * Talaria as the phone's "Default digital assistant app". The system starts and binds this only when the person has
 * chosen Talaria as the assistant (and only then calls the session below). It starts nothing of its own: no foreground
 * service, no polling, no wake lock. Android requires these services to exist; that is all this one is.
 */
class TalariaInteractionService : VoiceInteractionService()

/** Hands the system a new session each time the assistant is invoked (power-button long-press, the assist gesture). */
class TalariaSessionService : VoiceInteractionSessionService() {
    override fun onNewSession(args: Bundle?): VoiceInteractionSession = TalariaSession(this)
}

/**
 * One invocation. It shows nothing of its own: it opens Talaria's message box (or the voice conversation, if the person
 * asked for that) and ends. If Android supplies a screenshot (only when the person turned on "Use screenshot" for the
 * assistant) it is kept as a private picture for the message box to attach; it is never sent from here.
 */
class TalariaSession(private val ctx: Context) : VoiceInteractionSession(ctx) {
    private val handler = Handler(Looper.getMainLooper())
    private var opened = false
    private val giveUp = Runnable { open(null) }

    override fun onShow(args: Bundle?, showFlags: Int) {
        super.onShow(args, showFlags)
        opened = false
        // With a screenshot on its way, wait a moment for it; without one there is nothing to wait for.
        if (AssistRules.expectsScreenshot(showFlags)) handler.postDelayed(giveUp, AssistRules.WAIT_MS) else open(null)
    }

    override fun onHandleScreenshot(screenshot: Bitmap?) {
        if (opened) { screenshot?.recycle(); return }
        handler.removeCallbacks(giveUp)
        open(screenshot?.let { keep(it) })
    }

    override fun onHide() { handler.removeCallbacks(giveUp); super.onHide() }

    private fun open(shot: Uri?) {
        if (opened) return
        opened = true
        Handoff.put(AssistRules.draft(shot != null), shot)
        val listen = AssistRules.listen((ctx.applicationContext as TalariaApp).repo.store.assistListen, shot != null)
        val i = (if (listen) QuickAction.Talk else QuickAction.Assist).intent(ctx)
        // Into Talaria's own task (the one the launcher opens), not the separate "assistant" stack that startAssistantActivity makes,
        // so there is one app screen and one message box, not two.
        try { ctx.startActivity(i) } catch (_: Exception) { try { startAssistantActivity(i) } catch (_: Exception) { } }
        finish()
    }

    /** The screenshot as a small private file the message box can attach the way it attaches a photo: not in the gallery, only the latest kept. */
    private fun keep(bitmap: Bitmap): Uri? = try {
        val dir = File(ctx.cacheDir, "camera").apply { mkdirs() }
        dir.listFiles()?.forEach { runCatching { it.delete() } }
        val file = File(dir, "screen-${System.currentTimeMillis()}.jpg")
        file.outputStream().use { bitmap.compress(Bitmap.CompressFormat.JPEG, 85, it) }
        FileProvider.getUriForFile(ctx, "${ctx.packageName}.files", file)
    } catch (_: Exception) { null } finally { bitmap.recycle() }
}

/**
 * Android insists that an assistant names a speech recognizer. Talaria listens with the phone's own recognizer through
 * the normal SpeechRecognizer, so this one is never the choice: it declares no recognition intent and refuses if asked.
 */
class UnusedRecognitionService : RecognitionService() {
    override fun onStartListening(recognizerIntent: Intent?, listener: Callback?) { listener?.error(SpeechRecognizer.ERROR_CLIENT) }
    override fun onCancel(listener: Callback?) {}
    override fun onStopListening(listener: Callback?) {}
}
