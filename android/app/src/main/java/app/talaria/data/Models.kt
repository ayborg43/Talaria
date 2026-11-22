package app.talaria.data

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement

val AppJson = Json { ignoreUnknownKeys = true; isLenient = true; coerceInputValues = true; explicitNulls = false }

/** One thing in the conversation: what you said, what the agent wrote, a tool it ran, or a question waiting. */
@Serializable
data class Message(
    val id: Long = 0,
    val role: String = "",        // user, assistant, tool, approval
    val text: String = "",
    val at: String = "",
    val tool: String = "",
    val state: String = "",       // tool: running ok error; approval: pending allowed always denied expired
    val ms: Long = 0,
    val result: String = "",
    val artifact: JsonElement? = null,
    val token: String = "",
    val always: Boolean = false,
    val url: String = "",
    // An assistant reply says which model wrote it, what the router took the message for, and why that model was chosen.
    val profile: String = "",
    val model: String = "",
    val kind: String = "",
    val why: String = "",
    /** What you said of it: "up", "down", or "". */
    val vote: String = "",
)

@Serializable
data class Worker(
    val id: Long = 0,
    val kind: String = "",        // main, sub, task, goal, watch, job
    val label: String = "",
    val doing: String = "",
    val model: String = "",
    val ageMs: Long = 0,
)

@Serializable data class Profile(val name: String = "", val avatar: String = "", val theme: String = "", val onboarded: Boolean = false)

@Serializable
data class Channel(
    val title: String = "", val icon: String = "", val color: String = "", val purpose: String = "",
    val agent: String = "", val read_only: String = "",
)

@Serializable
data class StateDto(
    val messages: List<Message> = emptyList(),
    val seq: Long = 0,
    val epoch: Long = 0,
    val running: Boolean = false,
    val agents: List<Worker> = emptyList(),
    val model: String = "",
    val paused: Boolean = false,
    val zone: String = "",
    val version: String = "",
    val profile: Profile = Profile(),
    val side: String = "",
    val channel: Channel? = null,
    val suggestion: SuggestionDto? = null,
)

@Serializable
data class EventDto(
    val seq: Long = 0,
    val type: String = "",        // msg, typing, reset, run, agents
    val msg: Message? = null,
    val on: Boolean = false,
    val agents: List<Worker>? = null,
    val model: String = "",
)

@Serializable data class SuggestionSource(val title: String = "", val url: String = "")

@Serializable
data class SuggestionDto(
    val id: String = "", val title: String = "", val body: String = "",
    val sources: List<SuggestionSource> = emptyList(), val at: String = "", val startable: Boolean = false,
)

@Serializable
data class ChatMeta(
    val id: String = "", val title: String = "", val archived: Boolean = false, val main: Boolean = false,
    val side: String = "", val purpose: String = "", val agent: String = "", val icon: String = "", val color: String = "",
    val updated_at: String = "", val messages: Int = 0, val active: Boolean = false,
)

@Serializable data class ChatsDto(val chats: List<ChatMeta> = emptyList())

@Serializable
data class JobInfo(val id: String = "", val kind: String = "", val title: String = "", val sub: String = "", val state: String = "")

@Serializable
data class TaskInfo(
    val id: String = "", val name: String = "", val status: String = "", val failed: Boolean = false,
    val at: String = "", val reviewed: Boolean = false,
)

@Serializable
data class FinishedInfo(val kind: String = "", val label: String = "", val seconds: Long = 0, val at: String = "", val stopped: Boolean = false, val model: String = "")

@Serializable
data class AgentsDto(
    val agents: List<Worker> = emptyList(),
    val recent: List<FinishedInfo> = emptyList(),
    val jobs: List<JobInfo>? = null,
    val tasks: List<TaskInfo> = emptyList(),
)

@Serializable
data class RunSummary(
    val id: String = "", val job_id: String = "", val name: String = "", val kind: String = "", val status: String = "",
    val started: String = "", val ended: String = "", val steps: Int = 0, val files: Int = 0, val doing: String = "",
)

@Serializable data class RunsDto(val runs: List<RunSummary> = emptyList(), val available: Boolean = false)

@Serializable data class RunStep(val at: String = "", val tool: String = "", val detail: String = "")
@Serializable data class RunFile(val id: String = "", val name: String = "", val mime: String = "")
@Serializable data class RunSteer(val at: String = "", val text: String = "", val applied: Boolean = false)

@Serializable
data class RunDetail(
    val id: String = "", val job_id: String = "", val name: String = "", val kind: String = "", val status: String = "",
    val model: String = "", val prompt: String = "", val started: String = "", val ended: String = "",
    val steps: List<RunStep> = emptyList(), val skipped: Int = 0, val files: List<RunFile> = emptyList(),
    val result: String = "", val steers: List<RunSteer> = emptyList(), val parent: String = "",
)

@Serializable
data class PersonaTrackDto(
    val done: Int = 0, val failed: Int = 0, val running: Int = 0, val waiting: Int = 0,
    val last_what: String = "", val tokens: Long = 0, val eval: String = "",
)

@Serializable
data class PersonaDto(
    val id: String = "", val name: String = "", val icon: String = "", val color: String = "",
    val description: String = "", val instructions: String = "", val profile: String = "", val read_only: Boolean = false,
    val scopes: List<String> = emptyList(), val can_revert: Boolean = false, val track: PersonaTrackDto? = null,
)

@Serializable
data class PersonasDto(
    val agents: List<PersonaDto> = emptyList(), val available: Boolean = false,
    val colors: List<String> = emptyList(), val default_profile: String = "",
    val profiles: List<String> = emptyList(), val scope_groups: List<String> = emptyList(),
)

/** One card on the board: a piece of work, the agent that does it, and what its last run reported. */
@Serializable
data class CardDto(
    val id: String = "", val title: String = "", val body: String = "", val column: String = "backlog",
    val agent: String = "", val allow_risky: Boolean = false, val job: String = "", val status: String = "", val result: String = "",
)

@Serializable
data class BoardDto(
    val available: Boolean = false, val columns: List<String> = emptyList(), val cards: List<CardDto> = emptyList(),
    val agents: List<String> = emptyList(), val main: String = "main", val card: String = "",
)

@Serializable data class AgendaItem(val title: String = "", val start: String = "", val end: String = "", val allDay: Boolean = false, val where: String = "", val calendar: String = "")
@Serializable data class MailItem(val id: Long = 0, val from: String = "", val subject: String = "", val at: String = "", val unread: Boolean = false, val needs: Boolean = false, val why: String = "")
@Serializable data class MailDto(val items: List<MailItem> = emptyList(), val unread: Int = 0)
@Serializable data class FileMeta(val id: String = "", val name: String = "", val mime: String = "", val size: Long = 0, val origin: String = "", val kind: String = "", val created: String = "")
@Serializable data class FilesDto(val files: List<FileMeta> = emptyList(), val retention_hours: Int = 12)

@Serializable
data class TodayDto(
    val agenda: List<AgendaItem> = emptyList(), val agendaOn: Boolean = false,
    val files: List<FileMeta>? = null, val jobs: List<JobInfo>? = null, val mail: MailDto? = null,
    val pending: Int = 0, val now: String = "", val zone: String = "",
)

@Serializable data class LinkInfo(val name: String = "", val detail: String = "", val tag: String = "")

@Serializable
data class ControlDto(
    val version: String = "", val model: String = "", val uptimeSec: Long = 0, val paused: Boolean = false,
    val pending: Int = 0, val permissions: String = "", val links: List<LinkInfo>? = null,
)

@Serializable
data class VoiceDto(
    val available: Boolean = false, val provider: String = "", val model: String = "",
    val auto_send: Boolean = false, val max_seconds: Int = 120,
)

@Serializable data class RuleCategory(val id: String = "", val title: String = "", val detail: String = "", val mode: String = "default", val risky: Boolean = false)
@Serializable data class RulesDto(val modes: List<String> = emptyList(), val categories: List<RuleCategory> = emptyList())

@Serializable
data class RecipeDto(val id: String = "", val title: String = "", val summary: String = "", val group: String = "", val kind: String = "", val `when`: String = "", val days: String = "")

@Serializable data class RecipesDto(val available: Boolean = false, val groups: List<String> = emptyList(), val recipes: List<RecipeDto> = emptyList())
@Serializable data class SuggestionsDto(val available: Boolean = false, val suggestions: List<SuggestionDto> = emptyList())

@Serializable data class SignInField(val kind: String = "", val label: String = "", val new: Boolean = false)

@Serializable
data class BrowserTab(val id: String = "", val url: String = "", val signin: List<SignInField> = emptyList(), val login: Boolean = false)

@Serializable data class BrowserTabs(val tabs: List<BrowserTab> = emptyList(), val width: Int = 0, val height: Int = 0)

@Serializable data class ErrorBody(val error: String = "")
@Serializable data class TextBody(val text: String = "")

@Serializable data class NotesView(val entries: List<String> = emptyList(), val used: Int = 0, val limit: Int = 0)
@Serializable data class ProfileFields(val name: String = "", val tone: String = "", val avatar: String = "", val theme: String = "", val onboarded: Boolean = false)
@Serializable data class ProfileDto(val profile: ProfileFields = ProfileFields(), val user: NotesView = NotesView(), val memory: NotesView = NotesView())
@Serializable data class BriefDto(val available: Boolean = false, val enabled: Boolean = false, val at: String = "", val next: String = "", val last: String = "", val status: String = "")

// ---- what the models cost (GET /api/usage) ----

@Serializable
data class UsageTotals(val calls: Long = 0, @kotlinx.serialization.SerialName("in") val tokensIn: Long = 0, @kotlinx.serialization.SerialName("out") val tokensOut: Long = 0, val estimated: Long = 0)

/** One row of the report: a name, what it used and, where every call had a price, what that cost. */
@Serializable
data class UsageLine(val name: String = "", val totals: UsageTotals = UsageTotals(), val cost: Double = 0.0, val priced: Boolean = false)

@Serializable data class UsageDay(val date: String = "", val tokens: Long = 0, val cost: Double = 0.0)

@Serializable
data class UsageReport(
    val total: UsageLine = UsageLine(), val by_model: List<UsageLine> = emptyList(), val by_agent: List<UsageLine> = emptyList(),
    val by_source: List<UsageLine> = emptyList(), val by_kind: List<UsageLine> = emptyList(),
    val per_day: List<UsageDay> = emptyList(), val unpriced: List<String> = emptyList(),
)

/** A price the owner set for one profile, in dollars for a million tokens; null when none is set. */
@Serializable data class UsagePrice(val profile: String = "", val model: String = "", val price_in: Double? = null, val price_out: Double? = null)

@Serializable
data class UsageDto(val available: Boolean = false, val report: UsageReport = UsageReport(), val summary: String = "", val prices: List<UsagePrice> = emptyList())

// ---- while you were away (GET /api/digest) ----

@Serializable
data class DigestSchedule(val available: Boolean = false, val enabled: Boolean = false, val at: String = "", val weekly: Boolean = false, val next: String = "", val last: String = "")

@Serializable data class DigestDto(val schedule: DigestSchedule = DigestSchedule(), val text: String? = null)

// ---- a shared agent file (POST /api/agent-file) ----

@Serializable data class AgentPreview(val name: String = "", val icon: String = "", val instructions: String = "", val read_only: Boolean = false)
@Serializable data class AgentPreviewDto(val agent: AgentPreview = AgentPreview(), val exists: Boolean = false)

// ---- old conversations (GET /api/retention) ----

@Serializable
data class RetentionDto(
    val available: Boolean = false, val keep_days: Int = 0, val history_keep_days: Int = 0, val grace_days: Int = 30,
    val would_archive: Int = 0, val would_delete: Int = 0, val last_at: String = "",
    val last_archived: Int = 0, val last_deleted: Int = 0, val last_pruned: Int = 0, val can_prune_history: Boolean = false,
)
