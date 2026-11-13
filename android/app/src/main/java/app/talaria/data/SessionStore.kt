package app.talaria.data

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * What the app keeps on the phone: the server's address, the sign-in cookie, and a few choices. The cookie is the
 * only secret (the password is never stored); it is sealed with a key that lives in the Android Keystore and
 * cannot leave the device.
 */
/** What the API needs to know: where the server is, and the cookie that proves who is asking. */
interface Credentials {
    val baseUrl: String
    val cookie: String
}

class SessionStore(context: Context) : Credentials {
    private val prefs = context.getSharedPreferences("talaria", Context.MODE_PRIVATE)

    override var baseUrl: String
        get() = prefs.getString("base", "") ?: ""
        set(v) { prefs.edit().putString("base", v).apply() }

    /** "name=value", as the server set it. */
    override var cookie: String
        get() = prefs.getString("cookie", null)?.let { open(it) } ?: ""
        set(v) { prefs.edit().apply { if (v.isEmpty()) remove("cookie") else putString("cookie", seal(v)) }.apply() }

    val signedIn: Boolean get() = baseUrl.isNotEmpty() && cookie.isNotEmpty()

    init {
        // A phone signed in before servers could be saved: its one server joins the list.
        if (signedIn && cookieFor(baseUrl).isEmpty()) remember(baseUrl, cookie)
    }

    // ---- several servers: the phone keeps one session per server, and one of them is live at a time ----

    private fun saved(): org.json.JSONObject = try { org.json.JSONObject(prefs.getString("saved", "{}") ?: "{}") } catch (_: Exception) { org.json.JSONObject() }

    /** Every server this phone is signed in to, in the order they were added. */
    val savedServers: List<String> get() = saved().keys().asSequence().toList()

    fun cookieFor(origin: String): String = saved().optString(origin).takeIf { it.isNotEmpty() }?.let { open(it) } ?: ""

    @Synchronized fun remember(origin: String, cookie: String) {
        prefs.edit().putString("saved", saved().put(origin, seal(cookie)).toString()).apply()
    }

    @Synchronized fun forget(origin: String) {
        prefs.edit().putString("saved", saved().apply { remove(origin) }.toString()).apply()
    }

    /** Makes [origin] the live server. False when the phone has no session for it. */
    fun activate(origin: String): Boolean {
        val c = cookieFor(origin)
        if (c.isEmpty()) return false
        baseUrl = origin
        cookie = c
        return true
    }

    // ---- what the agent may read on this phone (every one off until switched on) ----

    var phoneOps: Set<String>
        get() = prefs.getStringSet("phoneOps", emptySet()) ?: emptySet()
        set(v) { prefs.edit().putStringSet("phoneOps", v).apply() }
    /** The last things the agent asked of this phone, as JSON, newest first. */
    var phoneLog: String
        get() = prefs.getString("phoneLog", "[]") ?: "[]"
        set(v) { prefs.edit().putString("phoneLog", v).apply() }

    // ---- voice: who recognises speech ----

    /** "phone" (the default: Android's own recognizer) or "server" (the speech service on the person's server). */
    var speechEngine: String
        get() = prefs.getString("speechEngine", "phone") ?: "phone"
        set(v) { prefs.edit().putString("speechEngine", v).apply() }
    /** With the phone's recognizer: only use one that runs on the phone, never an online one. On unless turned off. */
    var speechLocalOnly: Boolean
        get() = prefs.getBoolean("speechLocalOnly", true)
        set(v) { prefs.edit().putBoolean("speechLocalOnly", v).apply() }

    // ---- messages that could not be sent yet ----

    /** Messages written while the server could not be reached, as JSON, oldest first. */
    var outbox: String
        get() = prefs.getString("outbox", "[]") ?: "[]"
        set(v) { prefs.edit().putString("outbox", v).apply() }

    // ---- alerts ----

    /** Approvals already shown as a notification, so a reconnect or a second check never shows one twice. */
    @Synchronized fun announce(token: String): Boolean {
        val list = (prefs.getString("announced", "") ?: "").split('\n').filter { it.isNotEmpty() }
        if (token in list) return false
        prefs.edit().putString("announced", app.talaria.service.Alerts.remember(list, token).joinToString("\n")).apply()
        return true
    }

    /** Replies made while these hours last arrive without a sound. Approvals always sound. */
    var quietHours: Boolean
        get() = prefs.getBoolean("quiet", false)
        set(v) { prefs.edit().putBoolean("quiet", v).apply() }
    var quietFrom: Int
        get() = prefs.getInt("quietFrom", 22 * 60)
        set(v) { prefs.edit().putInt("quietFrom", v).apply() }
    var quietTo: Int
        get() = prefs.getInt("quietTo", 7 * 60)
        set(v) { prefs.edit().putInt("quietTo", v).apply() }

    var lockWithBiometrics: Boolean
        get() = prefs.getBoolean("lock", false)
        set(v) { prefs.edit().putBoolean("lock", v).apply() }
    var notifications: Boolean
        get() = prefs.getBoolean("notify", true)
        set(v) { prefs.edit().putBoolean("notify", v).apply() }
    var speakReplies: Boolean
        get() = prefs.getBoolean("speak", true)
        set(v) { prefs.edit().putBoolean("speak", v).apply() }
    var askedNotifications: Boolean
        get() = prefs.getBoolean("askedNotify", false)
        set(v) { prefs.edit().putBoolean("askedNotify", v).apply() }
    /** system, light or dark. */
    var theme: String
        get() = prefs.getString("theme", "system") ?: "system"
        set(v) { prefs.edit().putString("theme", v).apply() }
    /** Conversations pinned on this phone (the server has no notion of pins), by id. */
    var pinned: Set<String>
        get() = prefs.getStringSet("pinned", emptySet()) ?: emptySet()
        set(v) { prefs.edit().putStringSet("pinned", v).apply() }
    /** What the home-screen widget last showed, so it can be drawn at once while it looks again. */
    var widgetPending: Int
        get() = prefs.getInt("w_pending", 0)
        set(v) { prefs.edit().putInt("w_pending", v).apply() }
    var widgetTasks: Int
        get() = prefs.getInt("w_tasks", 0)
        set(v) { prefs.edit().putInt("w_tasks", v).apply() }
    var lastServers: String
        get() = prefs.getString("last", "") ?: ""
        set(v) { prefs.edit().putString("last", v).apply() }

    /** Forgets the live session and its saved copy. */
    fun signOut() { if (baseUrl.isNotEmpty()) forget(baseUrl); cookie = "" }

    private fun key(): SecretKey {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (ks.getKey(ALIAS, null) as? SecretKey)?.let { return it }
        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        gen.init(
            KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build(),
        )
        return gen.generateKey()
    }

    private fun seal(plain: String): String {
        val c = Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.ENCRYPT_MODE, key()) }
        val out = c.iv + c.doFinal(plain.toByteArray())
        return Base64.encodeToString(out, Base64.NO_WRAP)
    }

    private fun open(sealed: String): String? = try {
        val raw = Base64.decode(sealed, Base64.NO_WRAP)
        val c = Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, raw, 0, 12)) }
        String(c.doFinal(raw, 12, raw.size - 12))
    } catch (_: Exception) {
        null // a key that is gone (app data restored on another phone) means signing in again
    }

    private companion object { const val ALIAS = "talaria_session" }
}
