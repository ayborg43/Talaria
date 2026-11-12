package app.talaria.ui.components

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.withLink
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import app.talaria.ui.theme.Mono
import app.talaria.ui.theme.extra

// A small Markdown renderer for what a model writes: headings, paragraphs, lists, quotes, fenced code, tables, rules,
// and bold, italic, code and links inside a line. It draws text and never runs anything: a link opens only when tapped,
// and only if it is http or https.

private sealed interface Block {
    data class Para(val text: String) : Block
    data class Head(val level: Int, val text: String) : Block
    data class Item(val marker: String, val text: String, val depth: Int) : Block
    data class Quote(val text: String) : Block
    data class Code(val lang: String, val text: String) : Block
    data class Table(val rows: List<List<String>>) : Block
    data object Rule : Block
}

private val bulletRe = Regex("""^(\s*)([-*+]|\d{1,3}[.)])\s+(.*)$""")
private val headRe = Regex("""^(#{1,4})\s+(.*)$""")
private val ruleRe = Regex("""^\s*([-*_])\s*(\1\s*){2,}$""")

private fun parse(src: String): List<Block> {
    val out = mutableListOf<Block>()
    val lines = src.replace("\r\n", "\n").lines()
    var i = 0
    val para = StringBuilder()
    fun flush() { if (para.isNotBlank()) out += Block.Para(para.toString().trim()); para.clear() }
    while (i < lines.size) {
        val l = lines[i]
        when {
            l.trimStart().startsWith("```") -> {
                flush()
                val lang = l.trim().removePrefix("```").trim()
                val code = StringBuilder(); i++
                while (i < lines.size && !lines[i].trimStart().startsWith("```")) { code.appendLine(lines[i]); i++ }
                out += Block.Code(lang, code.toString().trimEnd('\n'))
            }
            l.isBlank() -> flush()
            ruleRe.matches(l) -> { flush(); out += Block.Rule }
            headRe.matches(l) -> { flush(); val m = headRe.find(l)!!; out += Block.Head(m.groupValues[1].length, m.groupValues[2]) }
            l.trimStart().startsWith(">") -> { flush(); out += Block.Quote(l.trimStart().removePrefix(">").trim()) }
            l.trimStart().startsWith("|") && i + 1 < lines.size && lines[i + 1].trim().matches(Regex("""\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?""")) -> {
                flush()
                val rows = mutableListOf<List<String>>()
                fun cells(s: String) = s.trim().trim('|').split('|').map { it.trim() }
                rows += cells(l); i += 2
                while (i < lines.size && lines[i].trimStart().startsWith("|")) { rows += cells(lines[i]); i++ }
                i--
                out += Block.Table(rows)
            }
            bulletRe.matches(l) -> {
                flush()
                val m = bulletRe.find(l)!!
                val marker = if (m.groupValues[2][0].isDigit()) m.groupValues[2] else "•"
                out += Block.Item(marker, m.groupValues[3], m.groupValues[1].length / 2)
            }
            else -> { if (para.isNotEmpty()) para.append(' '); para.append(l.trim()) }
        }
        i++
    }
    flush()
    return out
}

private val inlineRe = Regex("""(\*\*[^*\n]+\*\*|__[^_\n]+__|`[^`\n]+`|\*[^*\s][^*\n]*\*|\[[^\]\n]+]\([^)\s]+\)|https?://[^\s<>)\]]+)""")

@Composable
private fun inline(text: String, base: TextStyle, linkColor: Color, codeBg: Color): AnnotatedString = remember(text, linkColor, codeBg) {
    buildAnnotatedString {
        var last = 0
        for (m in inlineRe.findAll(text)) {
            if (m.range.first > last) append(text.substring(last, m.range.first))
            val t = m.value
            when {
                t.startsWith("**") || t.startsWith("__") -> withStyle(SpanStyle(fontWeight = FontWeight.SemiBold)) { append(t.drop(2).dropLast(2)) }
                t.startsWith("`") -> withStyle(SpanStyle(fontFamily = Mono, fontSize = base.fontSize * 0.9, background = codeBg)) { append(" ${t.trim('`')} ") }
                t.startsWith("[") -> {
                    val label = t.substring(1, t.indexOf(']')); val url = t.substring(t.indexOf("](") + 2, t.length - 1)
                    if (url.startsWith("http://") || url.startsWith("https://"))
                        withLink(LinkAnnotation.Url(url, TextLinkStyles(SpanStyle(color = linkColor, textDecoration = TextDecoration.Underline)))) { append(label) }
                    else append(label)
                }
                t.startsWith("http") -> {
                    val clean = t.trimEnd('.', ',', ';', ':', '!', '?')
                    withLink(LinkAnnotation.Url(clean, TextLinkStyles(SpanStyle(color = linkColor, textDecoration = TextDecoration.Underline)))) { append(clean) }
                    append(t.substring(clean.length))
                }
                else -> withStyle(SpanStyle(fontStyle = FontStyle.Italic)) { append(t.drop(1).dropLast(1)) }
            }
            last = m.range.last + 1
        }
        if (last < text.length) append(text.substring(last))
    }
}

@Composable
fun Markdown(text: String, modifier: Modifier = Modifier, color: Color = MaterialTheme.colorScheme.onSurface) {
    val blocks = remember(text) { parse(text) }
    val body = MaterialTheme.typography.bodyLarge.copy(color = color)
    val link = MaterialTheme.colorScheme.tertiary
    val codeBg = MaterialTheme.colorScheme.surfaceContainerHigh
    val outline = MaterialTheme.colorScheme.outlineVariant
    Column(modifier, verticalArrangement = Arrangement.spacedBy(10.dp)) {
        for (b in blocks) when (b) {
            is Block.Para -> Text(inline(b.text, body, link, codeBg), style = body)
            is Block.Head -> Text(
                inline(b.text, body, link, codeBg),
                style = when (b.level) { 1 -> MaterialTheme.typography.headlineSmall; 2 -> MaterialTheme.typography.titleLarge; else -> MaterialTheme.typography.titleMedium }.copy(color = color),
                modifier = Modifier.padding(top = 4.dp),
            )
            is Block.Item -> Row(Modifier.padding(start = (b.depth * 16).dp)) {
                if (b.marker == "•") Box(Modifier.width(18.dp).padding(top = 10.dp, start = 4.dp)) { Box(Modifier.size(5.dp).clip(CircleShape).background(MaterialTheme.colorScheme.onSurfaceVariant)) }
                else Text(b.marker, style = body.copy(color = MaterialTheme.colorScheme.onSurfaceVariant), modifier = Modifier.width(26.dp))
                Text(inline(b.text, body, link, codeBg), style = body, modifier = Modifier.weight(1f))
            }
            is Block.Quote -> Row(Modifier.fillMaxWidth().height(IntrinsicSize.Min)) {
                Box(Modifier.width(3.dp).fillMaxHeight().clip(CircleShape).background(MaterialTheme.extra.brassLine))
                Text(inline(b.text, body, link, codeBg), style = body.copy(color = MaterialTheme.colorScheme.onSurfaceVariant), modifier = Modifier.padding(start = 12.dp))
            }
            is Block.Code -> Box(
                Modifier.fillMaxWidth().clip(MaterialTheme.shapes.medium).background(codeBg).border(0.5.dp, outline.copy(alpha = 0.7f), MaterialTheme.shapes.medium)
                    .horizontalScroll(rememberScrollState()).padding(12.dp),
            ) { Text(b.text, style = TextStyle(fontFamily = Mono, fontSize = 13.sp, lineHeight = 19.sp, color = color)) }
            is Block.Table -> Box(Modifier.fillMaxWidth().clip(MaterialTheme.shapes.medium).border(0.5.dp, outline.copy(alpha = 0.7f), MaterialTheme.shapes.medium).horizontalScroll(rememberScrollState())) {
                Column {
                    b.rows.forEachIndexed { r, row ->
                        Row(Modifier.background(if (r == 0) codeBg else Color.Transparent)) {
                            row.forEach { cell ->
                                Text(
                                    inline(cell, body, link, codeBg),
                                    style = MaterialTheme.typography.bodyMedium.copy(color = color, fontWeight = if (r == 0) FontWeight.SemiBold else FontWeight.Normal),
                                    modifier = Modifier.width(132.dp).padding(horizontal = 10.dp, vertical = 8.dp),
                                )
                            }
                        }
                    }
                }
            }
            Block.Rule -> Box(Modifier.fillMaxWidth().padding(vertical = 4.dp).background(outline).padding(0.5.dp))
        }
    }
}

/** Plain words for speaking aloud or a notification: the same text with its marks taken off. */
fun plainText(md: String): String = md
    .replace(Regex("```[\\s\\S]*?```"), " code block. ")
    .replace(Regex("\\[([^\\]]+)]\\([^)]+\\)"), "$1")
    .replace(Regex("https?://\\S+"), " link ")
    .replace(Regex("[*_`#>|]+"), "")
    .replace(Regex("^\\s*[-+•]\\s+", RegexOption.MULTILINE), "")
    .replace(Regex("\\s+"), " ").trim()
