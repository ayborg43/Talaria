package app.talaria.service

import android.annotation.SuppressLint
import android.app.PendingIntent
import android.content.Intent
import android.graphics.drawable.Icon
import android.os.Build
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService
import app.talaria.QuickAction
import app.talaria.R
import app.talaria.TalariaApp

/** A quick-settings tile: pull the shade down, tap Talk, and a voice conversation starts. */
class TalkTile : TileService() {
    override fun onStartListening() {
        val tile = qsTile ?: return
        val signedIn = (application as TalariaApp).repo.store.signedIn
        tile.label = getString(R.string.tile_label)
        tile.icon = Icon.createWithResource(this, R.drawable.ic_tile_wave)
        tile.state = if (signedIn) Tile.STATE_INACTIVE else Tile.STATE_UNAVAILABLE
        if (Build.VERSION.SDK_INT >= 29) tile.subtitle = if (signedIn) "Talaria" else "Sign in first"
        tile.updateTile()
    }

    // The Intent form is only reached below Android 14, where it is the only one that exists.
    @SuppressLint("StartActivityAndCollapseDeprecated")
    override fun onClick() {
        if (!(application as TalariaApp).repo.store.signedIn) return
        val start = {
            val i: Intent = QuickAction.Talk.intent(this)
            if (Build.VERSION.SDK_INT >= 34) startActivityAndCollapse(PendingIntent.getActivity(this, 20, i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT))
            else @Suppress("DEPRECATION") startActivityAndCollapse(i)
        }
        if (isLocked) unlockAndRun(start) else start()
    }
}
