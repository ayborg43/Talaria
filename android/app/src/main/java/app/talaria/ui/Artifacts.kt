package app.talaria.ui

import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.doubleOrNull
import kotlinx.serialization.json.jsonObject

/** A typed result record a tool leaves beside its text (a mail, a plan, a document), read leniently: a missing field is empty. */
class Artifact(private val o: JsonObject) {
    val kind: String get() = str("kind")
    fun str(k: String): String = (o[k] as? JsonPrimitive)?.takeIf { it.isString || it.content != "null" }?.content.orEmpty()
    fun num(k: String): Double = (o[k] as? JsonPrimitive)?.doubleOrNull ?: str(k).toDoubleOrNull() ?: 0.0
    fun bool(k: String): Boolean = (o[k] as? JsonPrimitive)?.booleanOrNull ?: false
    fun list(k: String): List<Artifact> = (o[k] as? JsonArray).orEmpty().mapNotNull { (it as? JsonObject)?.let(::Artifact) }
    fun strings(k: String): List<String> = (o[k] as? JsonArray).orEmpty().mapNotNull { (it as? JsonPrimitive)?.content }

    companion object {
        val KINDS = setOf("mail_triage", "mail_list", "mail", "page", "plan", "spending", "document")
        fun of(e: JsonElement?): Artifact? {
            val o = runCatching { e?.jsonObject }.getOrNull() ?: return null
            return Artifact(o).takeIf { it.kind in KINDS }
        }
    }
}

/** "Amina Bello <amina@x.com>" -> "Amina Bello". */
fun senderName(from: String): String = from.replace(Regex("\\s*<.*>$"), "").trim().ifBlank { from.ifBlank { "(unknown)" } }
