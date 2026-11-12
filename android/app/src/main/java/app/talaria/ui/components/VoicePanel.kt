package app.talaria.ui.components

import app.talaria.ui.theme.Ti

import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import app.talaria.ui.theme.extra
import app.talaria.voice.Phase
import app.talaria.voice.VoiceController

/** What the conversation is doing right now, with an orb that swells with your voice. */
@Composable
fun VoicePanel(voice: VoiceController, modifier: Modifier = Modifier) {
    val phase by voice.phase.collectAsState()
    val level by voice.level.collectAsState()
    val note by voice.note.collectAsState()
    val brass = MaterialTheme.colorScheme.primary
    val swell by animateFloatAsState(
        when (phase) { Phase.Hearing -> 0.35f + level * 0.65f; Phase.Listening -> 0.15f + level * 0.5f; Phase.Speaking -> 0.4f; else -> 0.12f },
        tween(120), label = "swell",
    )
    val (label, hint) = when (phase) {
        Phase.Listening -> "Listening" to "Just talk. I will notice when you stop."
        Phase.Hearing -> "I hear you" to "Pause when you are done."
        Phase.Transcribing -> "Turning your voice into text" to ""
        Phase.Thinking -> "Thinking" to if (note.isNotBlank()) "“${note}”" else ""
        Phase.Speaking -> "Speaking" to "Talk to interrupt."
        Phase.Off -> "" to ""
    }
    Box(modifier.fillMaxWidth().padding(start = 12.dp, end = 12.dp, top = 6.dp, bottom = 10.dp), contentAlignment = Alignment.Center) {
        Surface(
            Modifier.widthIn(max = 720.dp).fillMaxWidth().shadow(10.dp, RoundedCornerShape(30.dp), ambientColor = Color(0x33000000), spotColor = Color(0x33000000)).semantics { liveRegion = LiveRegionMode.Polite },
            shape = RoundedCornerShape(30.dp), color = MaterialTheme.colorScheme.surfaceContainerLowest,
            border = BorderStroke(0.5.dp, MaterialTheme.extra.brassLine.copy(alpha = 0.7f)),
        ) {
            Row(Modifier.padding(start = 14.dp, end = 10.dp, top = 10.dp, bottom = 10.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                // A glow that swells with your voice, and a brighter core.
                Canvas(Modifier.size(60.dp)) {
                    val r = size.minDimension / 2
                    drawCircle(Brush.radialGradient(listOf(brass.copy(alpha = 0.40f), Color.Transparent), radius = r), radius = r * (0.78f + swell * 0.3f))
                    drawCircle(Brush.radialGradient(listOf(Color(0xFFFFD98A), brass), radius = r * 0.5f), radius = r * 0.44f * (1f + swell * 0.16f))
                }
                Column(Modifier.weight(1f)) {
                    Text(label, style = MaterialTheme.typography.titleSmall)
                    if (hint.isNotBlank()) Text(hint, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 2, overflow = TextOverflow.Ellipsis)
                }
                RoundIconButton(Ti.Close, "End the conversation", onClick = voice::stop)
            }
        }
    }
}
