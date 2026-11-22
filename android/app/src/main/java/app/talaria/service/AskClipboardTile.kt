package app.talaria.service

import android.annotation.SuppressLint
import android.app.PendingIntent
import android.content.Intent
import android.graphics.drawable.Icon
import android.os.Build
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService
import app.talaria.R
import app.talaria.TalariaApp
import app.talaria.assist.AskClipboardActivity

/** A quick-settings tile: copy some text, tap this, and Talaria opens a message about it. The clipboard is read only after the tap. */
class AskClipboardTile : TileService() {
    override fun onStartListening() {
        val tile = qsTile ?: return
        val signedIn = (application as TalariaApp).repo.store.signedIn
        tile.label = getString(R.string.tile_clip_label)
        tile.icon = Icon.createWithResource(this, R.drawable.ic_tile_clip)
        tile.state = if (signedIn) Tile.STATE_INACTIVE else Tile.STATE_UNAVAILABLE
        if (Build.VERSION.SDK_INT >= 29) tile.subtitle = if (signedIn) "Talaria" else "Sign in first"
        tile.updateTile()
    }

    @SuppressLint("StartActivityAndCollapseDeprecated")
    override fun onClick() {
        if (!(application as TalariaApp).repo.store.signedIn) return
        val start = {
            val i = Intent(this, AskClipboardActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_NO_HISTORY)
            if (Build.VERSION.SDK_INT >= 34) startActivityAndCollapse(PendingIntent.getActivity(this, 21, i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT))
            else @Suppress("DEPRECATION") startActivityAndCollapse(i)
        }
        if (isLocked) unlockAndRun(start) else start()
    }
}
