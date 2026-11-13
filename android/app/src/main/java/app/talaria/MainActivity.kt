package app.talaria

import android.Manifest
import android.content.Intent
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import androidx.core.splashscreen.SplashScreen.Companion.installSplashScreen
import android.graphics.Color
import androidx.activity.SystemBarStyle
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import app.talaria.service.ConnectionService
import app.talaria.ui.Root
import app.talaria.ui.theme.TalariaTheme
import app.talaria.voice.VoiceController

class MainActivity : AppCompatActivity() {
    private val voice: VoiceController get() = (application as TalariaApp).voice
    private var shared by mutableStateOf<Shared?>(null)
    private var locked by mutableStateOf(false)
    private var uploadError by mutableStateOf<String?>(null)
    private var quick by mutableStateOf<QuickAction?>(null)
    private var leftAt = 0L

    private val micPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) { ok ->
        if (ok) voice.micGranted() else voice.micDenied()
    }
    private val pickFile = registerForActivityResult(ActivityResultContracts.GetContent()) { uri -> if (uri != null) upload(uri) }
    private var photoUri: android.net.Uri? = null
    /** Set once a photo has been sent, so the message box asks about it. */
    private var photoSent by mutableStateOf(0)
    private val takePhoto = registerForActivityResult(ActivityResultContracts.TakePicture()) { ok ->
        val uri = photoUri; photoUri = null
        if (ok && uri != null) { upload(uri); photoSent++ }
    }

    /** Opens the camera. The picture goes to a private cache file, is sent like any other attachment, and is not kept on the phone's gallery. */
    fun capture() {
        if (!repo.store.signedIn) return
        val dir = java.io.File(cacheDir, "camera").apply { mkdirs() }
        dir.listFiles()?.forEach { runCatching { it.delete() } } // only the latest photo is ever kept here
        val file = java.io.File(dir, "photo-${System.currentTimeMillis()}.jpg")
        val uri = androidx.core.content.FileProvider.getUriForFile(this, "$packageName.files", file)
        photoUri = uri
        try { takePhoto.launch(uri) } catch (e: Exception) { photoUri = null; repo.toast("This phone has no camera app to take a picture with.") }
    }
    private var pendingSave: ByteArray? = null
    private val saveDocument = registerForActivityResult(ActivityResultContracts.CreateDocument("*/*")) { uri ->
        val bytes = pendingSave; pendingSave = null
        if (uri == null || bytes == null) return@registerForActivityResult
        lifecycleScope.launch {
            try { withContext(kotlinx.coroutines.Dispatchers.IO) { contentResolver.openOutputStream(uri)?.use { it.write(bytes) } ?: throw java.io.IOException("That place could not be written to.") }; repo.toast("Saved") }
            catch (e: Exception) { repo.toast(e.message ?: "That could not be saved.") }
        }
    }
    private val notifyPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) { ConnectionService.sync(this) }

    override fun onCreate(savedInstanceState: Bundle?) {
        // The splash holds for a moment while the first state arrives, so the first thing seen is the chat, not a blank.
        val splash = installSplashScreen()
        val began = android.os.SystemClock.uptimeMillis()
        splash.setKeepOnScreenCondition { repo.conn.value is app.talaria.data.Conn.Connecting && android.os.SystemClock.uptimeMillis() - began < 1500 }
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        locked = repo.store.lockWithBiometrics && repo.store.signedIn
        handleIntent(intent)
        setContent {
            val mode by repo.theme.collectAsState()
            val dark = when (mode) { "dark" -> true; "light" -> false; else -> isSystemInDarkTheme() }
            DisposableEffect(dark) {
                enableEdgeToEdge(
                    statusBarStyle = SystemBarStyle.auto(Color.TRANSPARENT, Color.TRANSPARENT) { dark },
                    navigationBarStyle = SystemBarStyle.auto(Color.TRANSPARENT, Color.TRANSPARENT) { dark },
                )
                onDispose { }
            }
            TalariaTheme(dark) {
                Root(
                    activity = this, repo = repo, voice = voice,
                    locked = locked, onUnlocked = { locked = false },
                    shared = shared, onSharedUsed = { shared = null },
                    onSaveFile = { name, _, bytes -> pendingSave = bytes; saveDocument.launch(name) },
                    quick = quick, onQuickHandled = { quick = null }, photoSent = photoSent, onTakePhoto = ::capture,
                    onAttach = { pickFile.launch("*/*") }, onUploadShared = { upload(it) }, uploadError = uploadError, onUploadErrorShown = { uploadError = null },
                    onNeedMic = { micPermission.launch(Manifest.permission.RECORD_AUDIO) },
                    onAskNotifications = { if (android.os.Build.VERSION.SDK_INT >= 33) notifyPermission.launch(Manifest.permission.POST_NOTIFICATIONS) else ConnectionService.sync(this) },
                )
            }
        }
    }

    /** Sends a chosen or shared file, and says in the chat screen's error line if it fails. */
    fun upload(uri: android.net.Uri) {
        lifecycleScope.launch {
            try { repo.uploadFrom(contentResolver, uri); uploadError = null } catch (e: SecurityException) { uploadError = "Talaria was not allowed to read that file. Try choosing it again." }
            catch (e: Exception) { uploadError = e.message ?: "That could not be sent." }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleIntent(intent)
    }

    private fun handleIntent(i: Intent?) {
        // A notification about another server switches to it first.
        i?.getStringExtra("server")?.let { repo.switchTo(it) }
        QuickAction.from(i?.action)?.let { if (it == QuickAction.Photo) capture() else quick = it; return }
        if (i?.action != Intent.ACTION_SEND) return
        val text = i.getStringExtra(Intent.EXTRA_TEXT)
        @Suppress("DEPRECATION") val stream = i.getParcelableExtra<android.net.Uri>(Intent.EXTRA_STREAM)
        shared = Shared(text, stream, i.type)
    }

    override fun onStart() {
        super.onStart()
        if (repo.store.lockWithBiometrics && repo.store.signedIn && leftAt > 0 && System.currentTimeMillis() - leftAt > LOCK_AFTER_MS) locked = true
        repo.start()
        ConnectionService.sync(this)
    }

    override fun onStop() {
        super.onStop()
        leftAt = System.currentTimeMillis()
        voice.stop() // the microphone is never left open behind another app
    }

    override fun onDestroy() { super.onDestroy(); if (isFinishing) voice.stop() }

    private companion object { const val LOCK_AFTER_MS = 60_000L }
}

/** Something another app shared to Talaria. */
data class Shared(val text: String?, val stream: android.net.Uri?, val type: String?)
