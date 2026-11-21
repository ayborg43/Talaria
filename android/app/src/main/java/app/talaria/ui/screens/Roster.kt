package app.talaria.ui.screens

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import app.talaria.data.CardDto
import app.talaria.data.PersonaDto
import app.talaria.ui.components.LiveDot
import app.talaria.ui.theme.extra

/**
 * What an agent is for, in a line: the first sentence of the instructions the person wrote it, with a leading "You are"
 * dropped (they are written to the agent, "You are a careful reviewer.", and read here as "Careful reviewer.").
 */
internal fun agentPurpose(instructions: String, max: Int = 90): String {
    val first = instructions.lineSequence().map { it.trim() }.firstOrNull { it.isNotEmpty() }.orEmpty()
    val end = Regex("[.!?](\\s|$)").find(first)?.range?.first
    var s = if (end != null && end > 0) first.substring(0, end + 1) else first
    s = s.replace(Regex("^(?i)you are (an? |the )?"), "").replaceFirstChar { it.uppercase() }
    return if (s.length > max) s.take(max - 1).trimEnd() + "…" else s
}

/**
 * Which agents have a background task running now, by name. The only cheap signal there is for this is the board: a card
 * in Running that is given to an agent. A task the main agent handed to one with `delegate` is not on the board, so it is
 * not shown: no dot is better than a made-up one.
 */
internal fun workingAgents(cards: List<CardDto>): Set<String> =
    cards.filter { it.column == "running" && it.agent.isNotBlank() }.map { it.agent.lowercase() }.toSet()

/**
 * The saved agents as a contacts list: an icon, a name, what it is for, and a small dot while a task of its is running.
 * Meant to sit at the top of the conversation list; tapping a row starts a conversation with that agent.
 */
internal fun LazyListScope.rosterItems(agents: List<PersonaDto>, working: Set<String>, showConversationsLabel: Boolean, onChat: (PersonaDto) -> Unit) {
    item(key = "roster-title") {
        Text("Agents", Modifier.padding(start = 8.dp, top = 4.dp, bottom = 2.dp), style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
    agents.forEachIndexed { i, a ->
        item(key = "agent-${a.id}") {
            if (i > 0) HorizontalDivider(Modifier.padding(start = 68.dp, end = 8.dp), color = MaterialTheme.colorScheme.outlineVariant.copy(alpha = 0.5f))
            AgentContact(a, a.name.lowercase() in working) { onChat(a) }
        }
    }
    if (showConversationsLabel) item(key = "roster-end") { Text("Conversations", Modifier.padding(start = 8.dp, top = 14.dp, bottom = 2.dp), style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSurfaceVariant) }
}

@androidx.compose.runtime.Composable
private fun AgentContact(a: PersonaDto, working: Boolean, onClick: () -> Unit) {
    val c = agentColor(a.color, MaterialTheme.extra, MaterialTheme.colorScheme)
    val purpose = agentPurpose(a.instructions)
    Row(
        Modifier.fillMaxWidth().clip(MaterialTheme.shapes.large).clickable(onClick = onClick).heightIn(min = 60.dp).padding(horizontal = 8.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Box(Modifier.size(44.dp).clip(CircleShape).background(c.copy(alpha = 0.22f)), contentAlignment = Alignment.Center) {
            Text(a.icon.ifBlank { a.name.take(1).uppercase() }, style = MaterialTheme.typography.titleMedium, color = c)
        }
        Column(Modifier.weight(1f)) {
            Text(a.name, style = MaterialTheme.typography.titleSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (purpose.isNotEmpty()) Text(purpose, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        if (working) LiveDot(modifier = Modifier.semantics { contentDescription = "${a.name} is working on a task now" })
    }
}
