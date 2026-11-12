package app.talaria.ui.theme

import androidx.compose.material3.ColorScheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.Color

// The same palette as the web app (internal/web/static/app.css): warm paper and ink, with brass as the one colour
// that means "act" or "you are here". Blue is information, green is done or working, red is refused or failed.
// Material roles carry the brand, so the system's own components pick it up without any one-off colours.

/** Colours the Material roles have no name for. */
@Immutable
class Extra(
    val ok: Color, val okTint: Color, val okLine: Color,
    val info: Color, val infoTint: Color, val infoInk: Color,
    val bad: Color, val badTint: Color, val badLine: Color,
    val brassText: Color, val brassTint: Color, val brassSoft: Color, val brassLine: Color,
    val bubble: Color, val bubbleLine: Color, val coral: Color, val onCoral: Color,
    val scrim: Color, val logo: Color,
)

val LightExtra = Extra(
    ok = Color(0xFF0B7643), okTint = Color(0xFFDAF7E3), okLine = Color(0xFFA4DEB7),
    info = Color(0xFF2855AD), infoTint = Color(0xFFE2ECFD), infoInk = Color(0xFF254582),
    bad = Color(0xFFB6322B), badTint = Color(0xFFFFE8E5), badLine = Color(0xFFF8BDB4),
    brassText = Color(0xFF7D5002), brassTint = Color(0xFFFBE6AF), brassSoft = Color(0xFFFEF6DC), brassLine = Color(0xFFE7C97E),
    bubble = Color(0xFFEFE9DC), bubbleLine = Color(0xFFDED5C3), coral = Color(0xFFF87966), onCoral = Color(0xFF250E0B),
    scrim = Color(0x6117150F), logo = Color(0xFF1B1812),
)

val DarkExtra = Extra(
    ok = Color(0xFF68CD8F), okTint = Color(0xFF143320), okLine = Color(0xFF275639),
    info = Color(0xFF8EB4FB), infoTint = Color(0xFF202E47), infoInk = Color(0xFFB2CBF9),
    bad = Color(0xFFFA897B), badTint = Color(0xFF47211C), badLine = Color(0xFF7A3A33),
    brassText = Color(0xFFEEC060), brassTint = Color(0xFF47340D), brassSoft = Color(0xFF2B210E), brassLine = Color(0xFF725518),
    bubble = Color(0xFF2B271F), bubbleLine = Color(0xFF3F3A2F), coral = Color(0xFFF87966), onCoral = Color(0xFF250E0B),
    scrim = Color(0x99000000), logo = Color(0xFF070603),
)

val LocalExtra = staticCompositionLocalOf { LightExtra }

val LightScheme: ColorScheme = lightColorScheme(
    primary = Color(0xFFEEB53E), onPrimary = Color(0xFF1D1406),
    primaryContainer = Color(0xFFFBE6AF), onPrimaryContainer = Color(0xFF4A3000),
    secondary = Color(0xFF5F5A51), onSecondary = Color(0xFFFEFDF9),
    secondaryContainer = Color(0xFFEFE9DC), onSecondaryContainer = Color(0xFF1B1812),
    tertiary = Color(0xFF2855AD), onTertiary = Color(0xFFFFFFFF),
    tertiaryContainer = Color(0xFFE2ECFD), onTertiaryContainer = Color(0xFF254582),
    background = Color(0xFFF8F3EB), onBackground = Color(0xFF1B1812),
    surface = Color(0xFFF8F3EB), onSurface = Color(0xFF1B1812),
    surfaceVariant = Color(0xFFF1ECE3), onSurfaceVariant = Color(0xFF5F5A51),
    surfaceTint = Color(0xFFEEB53E),
    surfaceContainerLowest = Color(0xFFFFFDFA), surfaceContainerLow = Color(0xFFFEFDF9),
    surfaceContainer = Color(0xFFFCF9F4), surfaceContainerHigh = Color(0xFFF1ECE3), surfaceContainerHighest = Color(0xFFEBE6DB),
    outline = Color(0xFF948B7B), outlineVariant = Color(0xFFE3DCCE),
    error = Color(0xFFB6322B), onError = Color(0xFFFFFFFF),
    errorContainer = Color(0xFFFFE8E5), onErrorContainer = Color(0xFF7A1F1A),
    inverseSurface = Color(0xFF1B1812), inverseOnSurface = Color(0xFFF8F3EB), inversePrimary = Color(0xFFEEB53E),
    scrim = Color(0xFF17150F),
)

val DarkScheme: ColorScheme = darkColorScheme(
    primary = Color(0xFFEEB53E), onPrimary = Color(0xFF1D1406),
    primaryContainer = Color(0xFF47340D), onPrimaryContainer = Color(0xFFFBE6AF),
    secondary = Color(0xFFA9A49A), onSecondary = Color(0xFF100E0A),
    secondaryContainer = Color(0xFF2B271F), onSecondaryContainer = Color(0xFFF1ECE4),
    tertiary = Color(0xFF8EB4FB), onTertiary = Color(0xFF0E1B33),
    tertiaryContainer = Color(0xFF202E47), onTertiaryContainer = Color(0xFFB2CBF9),
    background = Color(0xFF100E0A), onBackground = Color(0xFFF1ECE4),
    surface = Color(0xFF100E0A), onSurface = Color(0xFFF1ECE4),
    surfaceVariant = Color(0xFF24211B), onSurfaceVariant = Color(0xFFA9A49A),
    surfaceTint = Color(0xFFEEB53E),
    surfaceContainerLowest = Color(0xFF0A0906), surfaceContainerLow = Color(0xFF1C1914),
    surfaceContainer = Color(0xFF16140F), surfaceContainerHigh = Color(0xFF24211B), surfaceContainerHighest = Color(0xFF2E2A23),
    outline = Color(0xFF7A7467), outlineVariant = Color(0xFF343028),
    error = Color(0xFFFA897B), onError = Color(0xFF3B0F0B),
    errorContainer = Color(0xFF47211C), onErrorContainer = Color(0xFFFFDAD4),
    inverseSurface = Color(0xFFECE7DF), inverseOnSurface = Color(0xFF191610), inversePrimary = Color(0xFF7D5002),
    scrim = Color(0xFF000000),
)
