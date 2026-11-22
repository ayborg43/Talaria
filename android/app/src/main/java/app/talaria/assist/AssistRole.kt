package app.talaria.assist

import android.app.Activity
import android.app.role.RoleManager
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.graphics.drawable.Icon
import android.os.Build
import android.provider.Settings
import app.talaria.R

/**
 * Whether Talaria is the phone's assistant, and the ways to get to where that is chosen. Android keeps the choice;
 * an app cannot make itself the assistant.
 */
object AssistRole {
    fun isHeld(ctx: Context): Boolean = try {
        if (Build.VERSION.SDK_INT >= 29) ctx.getSystemService(RoleManager::class.java)?.isRoleHeld(RoleManager.ROLE_ASSISTANT) == true
        else Settings.Secure.getString(ctx.contentResolver, "assistant")?.startsWith(ctx.packageName + "/") == true
    } catch (_: Exception) { false }

    /** Opens "Default digital assistant app". False when the phone has no such page. */
    fun openChooser(activity: Activity): Boolean {
        for (action in listOf(Settings.ACTION_VOICE_INPUT_SETTINGS, Settings.ACTION_MANAGE_DEFAULT_APPS_SETTINGS)) {
            try { activity.startActivity(Intent(action)); return true } catch (_: Exception) { }
        }
        return false
    }

    /** On Android 13 and later asks to add the Ask clipboard tile to Quick Settings; before that says how to add it by hand. */
    fun offerClipboardTile(activity: Activity, say: (String) -> Unit) {
        val by = "Swipe down twice, tap the pencil, and drag the Ask clipboard tile into the panel."
        if (Build.VERSION.SDK_INT < 33) { say(by); return }
        try {
            val bar = activity.getSystemService(android.app.StatusBarManager::class.java)
            bar.requestAddTileService(
                ComponentName(activity, app.talaria.service.AskClipboardTile::class.java), activity.getString(R.string.tile_clip_label),
                Icon.createWithResource(activity, R.drawable.ic_tile_clip), activity.mainExecutor,
            ) { code ->
                say(when (code) {
                    android.app.StatusBarManager.TILE_ADD_REQUEST_RESULT_TILE_ADDED -> "Added to Quick Settings."
                    android.app.StatusBarManager.TILE_ADD_REQUEST_RESULT_TILE_ALREADY_ADDED -> "It is already in Quick Settings."
                    else -> by
                })
            }
        } catch (_: Exception) { say(by) }
    }
}
