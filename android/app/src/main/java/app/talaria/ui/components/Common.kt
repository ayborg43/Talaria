package app.talaria.ui.components

import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.BorderStroke
import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.Spring
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.spring
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.interaction.collectIsPressedAsState
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.RowScope
import androidx.compose.material3.ButtonColors
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.IconButtonDefaults
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.ui.composed
import androidx.compose.runtime.remember
import androidx.compose.ui.draw.drawWithCache
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Shape
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.hapticfeedback.HapticFeedback
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import app.talaria.ui.theme.extra

/** The mark: three brass strokes on ink, drawn from the same three curves as the app icon and the web app. */
@Composable
fun TalariaMark(size: Dp = 56.dp, modifier: Modifier = Modifier) {
    Canvas(modifier.size(size).clip(CircleShape).background(MaterialTheme.extra.logo)) {
        val u = this.size.minDimension / 32f
        val brass = Color(0xFFF2B544)
        // Content sits inside the circle, scaled to 70% and centred, so the strokes never touch its edge.
        val s = 0.7f; val o = (1 - s) * 16f
        fun stroke(x0: Float, y0: Float, cx1: Float, cy1: Float, cx2: Float, cy2: Float, x1: Float, y1: Float) {
            val p = Path().apply { moveTo(x0 * u, y0 * u); cubicTo(cx1 * u, cy1 * u, cx2 * u, cy2 * u, x1 * u, y1 * u) }
            drawPath(p, brass, style = Stroke(width = 2.6f * s * u, cap = StrokeCap.Round))
        }
        fun t(v: Float, off: Float) = v * s + off
        stroke(t(3f, o), t(21f, o), t(9f, o), t(21f, o), t(15f, o), t(18f, o), t(21f, o), t(9f, o))
        stroke(t(7f, o), t(25f, o), t(13f, o), t(25f, o), t(20f, o), t(21f, o), t(27f, o), t(12f, o))
        stroke(t(12f, o), t(28f, o), t(18f, o), t(28f, o), t(24f, o), t(24f, o), t(29f, o), t(17f, o))
    }
}

/** A raised surface with a hairline edge: the card of the web app. */
@Composable
fun Panel(
    modifier: Modifier = Modifier,
    tint: Color = MaterialTheme.colorScheme.surfaceContainerLow,
    edge: Color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.55f),
    content: @Composable () -> Unit,
) {
    Surface(modifier, shape = MaterialTheme.shapes.large, color = tint, border = BorderStroke(0.5.dp, edge), content = content)
}

@Composable
fun SectionTitle(text: String, modifier: Modifier = Modifier, trailing: @Composable (() -> Unit)? = null) {
    Row(modifier.fillMaxWidth().padding(start = 4.dp, top = 22.dp, bottom = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(text, style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.weight(1f))
        trailing?.invoke()
    }
}

enum class Tone { Neutral, Brass, Ok, Info, Bad }

@Composable
fun toneColors(tone: Tone): Pair<Color, Color> {
    val c = MaterialTheme.colorScheme; val x = MaterialTheme.extra
    return when (tone) {
        Tone.Neutral -> c.surfaceContainerHigh to c.onSurfaceVariant
        Tone.Brass -> x.brassTint to x.brassText
        Tone.Ok -> x.okTint to x.ok
        Tone.Info -> x.infoTint to x.infoInk
        Tone.Bad -> x.badTint to x.bad
    }
}

/** A small label: a state, a kind, a count. */
@Composable
fun Pill(text: String, tone: Tone = Tone.Neutral, modifier: Modifier = Modifier, icon: ImageVector? = null) {
    val (bg, fg) = toneColors(tone)
    Row(
        modifier.clip(CircleShape).background(bg).padding(horizontal = 10.dp, vertical = 4.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        if (icon != null) Icon(icon, null, Modifier.size(14.dp), tint = fg)
        Text(text, style = MaterialTheme.typography.labelMedium, color = fg, maxLines = 1)
    }
}

/** A dot that breathes while something is working. */
@Composable
fun LiveDot(color: Color = MaterialTheme.extra.ok, modifier: Modifier = Modifier, active: Boolean = true) {
    val t = rememberInfiniteTransition(label = "dot")
    val a by t.animateFloat(1f, 0.35f, infiniteRepeatable(tween(900), RepeatMode.Reverse), label = "alpha")
    Box(modifier.size(8.dp).alpha(if (active) a else 1f).clip(CircleShape).background(color))
}

/** An empty list says what it is for and what to do, in the product's own words. */
@Composable
fun EmptyState(title: String, body: String, modifier: Modifier = Modifier, action: @Composable (() -> Unit)? = null) {
    Column(modifier.fillMaxWidth().padding(horizontal = 32.dp, vertical = 40.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        Text(title, style = MaterialTheme.typography.titleLarge, textAlign = TextAlign.Center)
        Spacer(Modifier.height(8.dp))
        Text(body, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.onSurfaceVariant, textAlign = TextAlign.Center)
        if (action != null) { Spacer(Modifier.height(16.dp)); action() }
    }
}

@Composable
fun ErrorNote(text: String, modifier: Modifier = Modifier) {
    val x = MaterialTheme.extra
    Surface(modifier.fillMaxWidth(), shape = MaterialTheme.shapes.medium, color = x.badTint, border = BorderStroke(1.dp, x.badLine)) {
        Text(text, Modifier.padding(12.dp), style = MaterialTheme.typography.bodyMedium, color = x.bad)
    }
}

@Composable
fun Hairline(modifier: Modifier = Modifier) {
    Box(modifier.fillMaxWidth().height(1.dp).background(MaterialTheme.colorScheme.outlineVariant))
}

@Composable
fun Gap(h: Dp = 8.dp, w: Dp = 0.dp) { Spacer(Modifier.height(h).width(w)) }


// ---- controls ----
// Brass is the colour of fills and of "you are here"; as text on paper it is too pale to read, so text buttons use the deep
// brass and secondary buttons use a tonal fill with ink. Every button gives a little under the finger, with a spring.

/** Scale that eases down while pressed and springs back. */
fun Modifier.pressScale(source: MutableInteractionSource, pressed: Float = 0.96f): Modifier = composed {
    val down by source.collectIsPressedAsState()
    val k by animateFloatAsState(if (down) pressed else 1f, spring(dampingRatio = 0.55f, stiffness = Spring.StiffnessMediumLow), label = "press")
    graphicsLayer { scaleX = k; scaleY = k }
}

@Composable
fun Button(
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    shape: Shape = CircleShape,
    colors: ButtonColors = ButtonDefaults.buttonColors(),
    contentPadding: PaddingValues = PaddingValues(horizontal = 22.dp, vertical = 10.dp),
    content: @Composable RowScope.() -> Unit,
) {
    val src = remember { MutableInteractionSource() }
    androidx.compose.material3.Button(
        onClick, modifier.heightIn(min = 48.dp).pressScale(src), enabled, shape, colors, elevation = null,
        contentPadding = contentPadding, interactionSource = src, content = content,
    )
}

/** The quieter button: a tonal fill, ink text. */
@Composable
fun SecondaryButton(
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    shape: Shape = CircleShape,
    colors: ButtonColors = ButtonDefaults.filledTonalButtonColors(containerColor = MaterialTheme.colorScheme.surfaceContainerHigh, contentColor = MaterialTheme.colorScheme.onSurface),
    contentPadding: PaddingValues = PaddingValues(horizontal = 20.dp, vertical = 10.dp),
    content: @Composable RowScope.() -> Unit,
) {
    val src = remember { MutableInteractionSource() }
    androidx.compose.material3.FilledTonalButton(
        onClick, modifier.heightIn(min = 48.dp).pressScale(src), enabled, shape, colors, elevation = null,
        contentPadding = contentPadding, interactionSource = src, content = content,
    )
}

@Composable
fun TextButton(
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    colors: ButtonColors = ButtonDefaults.textButtonColors(contentColor = MaterialTheme.extra.brassText),
    contentPadding: PaddingValues = ButtonDefaults.TextButtonContentPadding,
    content: @Composable RowScope.() -> Unit,
) = androidx.compose.material3.TextButton(onClick, modifier.heightIn(min = 48.dp), enabled, shape = CircleShape, colors = colors, contentPadding = contentPadding, content = content)

/** A round icon button with a quiet fill. */
@Composable
fun RoundIconButton(icon: ImageVector, label: String, onClick: () -> Unit, modifier: Modifier = Modifier, size: Dp = 44.dp, enabled: Boolean = true, container: Color = MaterialTheme.colorScheme.surfaceContainerHigh, content: Color = MaterialTheme.colorScheme.onSurface) {
    val src = remember { MutableInteractionSource() }
    androidx.compose.material3.FilledIconButton(
        onClick, modifier.size(size).pressScale(src, 0.92f), enabled,
        colors = IconButtonDefaults.filledIconButtonColors(containerColor = container, contentColor = content, disabledContainerColor = MaterialTheme.colorScheme.surfaceContainerHigh, disabledContentColor = MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = 0.5f)),
        interactionSource = src,
    ) { Icon(icon, label, Modifier.size(size * 0.5f)) }
}

@Composable
fun OutlinedTextField(
    value: String,
    onValueChange: (String) -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    label: (@Composable () -> Unit)? = null,
    placeholder: (@Composable () -> Unit)? = null,
    trailingIcon: (@Composable () -> Unit)? = null,
    leadingIcon: (@Composable () -> Unit)? = null,
    supportingText: (@Composable () -> Unit)? = null,
    visualTransformation: androidx.compose.ui.text.input.VisualTransformation = androidx.compose.ui.text.input.VisualTransformation.None,
    keyboardOptions: androidx.compose.foundation.text.KeyboardOptions = androidx.compose.foundation.text.KeyboardOptions.Default,
    keyboardActions: androidx.compose.foundation.text.KeyboardActions = androidx.compose.foundation.text.KeyboardActions.Default,
    singleLine: Boolean = false,
    maxLines: Int = if (singleLine) 1 else Int.MAX_VALUE,
    minLines: Int = 1,
    shape: Shape = MaterialTheme.shapes.medium,
) = androidx.compose.material3.OutlinedTextField(
    value, onValueChange, modifier, enabled, label = label, placeholder = placeholder, leadingIcon = leadingIcon, trailingIcon = trailingIcon, supportingText = supportingText,
    visualTransformation = visualTransformation, keyboardOptions = keyboardOptions, keyboardActions = keyboardActions, singleLine = singleLine,
    maxLines = maxLines, minLines = minLines, shape = shape, colors = fieldColors(),
)

/** Text field colours: a soft edge, with the deep brass as the focus accent (the pale brass would not reach 3:1 against paper). */
@Composable
fun fieldColors() = OutlinedTextFieldDefaults.colors(
    focusedBorderColor = MaterialTheme.extra.brassText, focusedLabelColor = MaterialTheme.extra.brassText, cursorColor = MaterialTheme.extra.brassText,
    unfocusedBorderColor = MaterialTheme.colorScheme.outline.copy(alpha = 0.6f), errorBorderColor = MaterialTheme.extra.bad,
    focusedContainerColor = MaterialTheme.colorScheme.surfaceContainerLowest, unfocusedContainerColor = MaterialTheme.colorScheme.surfaceContainerLowest,
)

// ---- feel ----

/** A moving highlight over a placeholder shape, shown while something loads. */
fun Modifier.shimmer(): Modifier = composed {
    val t = rememberInfiniteTransition(label = "shimmer")
    val x by t.animateFloat(-1f, 2f, infiniteRepeatable(tween(1400, easing = LinearEasing)), label = "x")
    val base = MaterialTheme.colorScheme.surfaceContainerHigh
    val lift = MaterialTheme.colorScheme.surfaceContainerHighest
    drawWithCache {
        val w = size.width
        val brush = Brush.horizontalGradient(listOf(base, lift, base), startX = w * (x - 0.5f), endX = w * (x + 0.5f))
        onDrawBehind { drawRect(brush) }
    }
}

@Composable
fun SkeletonBlock(modifier: Modifier = Modifier, height: Dp = 16.dp) {
    Box(modifier.fillMaxWidth().height(height).clip(MaterialTheme.shapes.small).shimmer())
}

/** Placeholder rows in the shape of a card, so a screen is never a blank wait. */
@Composable
fun SkeletonCard(modifier: Modifier = Modifier, lines: Int = 2) {
    Panel(modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            SkeletonBlock(Modifier.fillMaxWidth(0.45f), 14.dp)
            repeat(lines) { i -> SkeletonBlock(Modifier.fillMaxWidth(if (i == lines - 1) 0.7f else 1f), 12.dp) }
        }
    }
}

/** Small taps of feedback in the hand: a firm one for yes, a sharper one for no, a light one for a switch. */
class Haptics(private val h: HapticFeedback) {
    fun confirm() = h.performHapticFeedback(HapticFeedbackType.Confirm)
    fun reject() = h.performHapticFeedback(HapticFeedbackType.Reject)
    fun tick() = h.performHapticFeedback(HapticFeedbackType.SegmentTick)
}

@Composable
fun rememberHaptics(): Haptics { val h = LocalHapticFeedback.current; return remember(h) { Haptics(h) } }

/** A switch with a quiet off state (no heavy outline) and a white thumb on the brass track. */
@Composable
fun Switch(checked: Boolean, onCheckedChange: ((Boolean) -> Unit)?, modifier: Modifier = Modifier, enabled: Boolean = true) =
    androidx.compose.material3.Switch(
        checked, onCheckedChange, modifier, enabled = enabled,
        colors = androidx.compose.material3.SwitchDefaults.colors(
            checkedTrackColor = MaterialTheme.colorScheme.primary, checkedThumbColor = Color.White, checkedBorderColor = Color.Transparent,
            uncheckedTrackColor = MaterialTheme.colorScheme.surfaceContainerHighest, uncheckedThumbColor = MaterialTheme.colorScheme.outline, uncheckedBorderColor = Color.Transparent,
            disabledCheckedTrackColor = MaterialTheme.colorScheme.primary.copy(alpha = 0.4f), disabledUncheckedTrackColor = MaterialTheme.colorScheme.surfaceContainerHigh,
            disabledUncheckedBorderColor = Color.Transparent, disabledCheckedBorderColor = Color.Transparent,
        ),
    )
