package app.talaria

import app.talaria.data.CardDto
import app.talaria.ui.Mentions
import app.talaria.ui.screens.agentPurpose
import app.talaria.ui.screens.workingAgents
import app.talaria.widget.BoardWidgetState
import app.talaria.widget.boardWidgetNote
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class RosterTest {
    private val names = listOf("Researcher", "Reviewer", "Inbox helper", "Research Partner", "Writer")

    @Test fun anAtOffersTheAgentsWhoseNamesBeginWithWhatWasTyped() {
        assertEquals(names, Mentions.suggest("@", names))
        assertEquals(listOf("Researcher", "Reviewer", "Research Partner"), Mentions.suggest("@Re", names))
        assertEquals(listOf("Researcher", "Research Partner"), Mentions.suggest("hello @research", names)) // any case, after other words
        assertEquals(listOf("Writer"), Mentions.suggest("@w", names))
        assertEquals(emptyList<String>(), Mentions.suggest("@zzz", names))
        assertEquals(listOf("Inbox helper"), Mentions.suggest("@Inbox h", names)) // a name with a space is still being typed
    }

    @Test fun atMostEightAreOffered() {
        val many = (1..20).map { "Agent $it" }
        assertEquals(Mentions.MAX, Mentions.suggest("@A", many).size)
        assertEquals(8, Mentions.MAX)
    }

    @Test fun nothingIsOfferedUnlessTheLastWordIsAnAt() {
        assertEquals(emptyList<String>(), Mentions.suggest("", names))
        assertEquals(emptyList<String>(), Mentions.suggest("no mention here", names))
        assertEquals(emptyList<String>(), Mentions.suggest("write to amina@re", names))   // an email address is not a mention
        assertFalse(Mentions.typing("amina@example.com"))
        assertEquals(emptyList<String>(), Mentions.suggest("@Reviewer\nand then more", names)) // a new line ends it
        assertEquals(emptyList<String>(), Mentions.suggest("@" + "x".repeat(40), names))
        assertTrue(Mentions.typing("first line\n@R"))
    }

    @Test fun anAgentThatWasChosenIsNotOfferedAgain() {
        assertEquals(emptyList<String>(), Mentions.suggest("@Writer ", names))
        assertEquals(emptyList<String>(), Mentions.suggest("@Researcher ", listOf("Researcher", "Researcher Two")))
        assertEquals(listOf("Writer"), Mentions.suggest("@Writer", names)) // still being typed: offered, so a tap adds the space
    }

    @Test fun choosingAnAgentWritesItsNameAndASpaceInPlaceOfWhatWasTyped() {
        assertEquals("@Reviewer ", Mentions.insert("@Re", "Reviewer"))
        assertEquals("ask @Writer ", Mentions.insert("ask @wr", "Writer"))
        assertEquals("@Inbox helper ", Mentions.insert("@Inbox h", "Inbox helper"))
        assertEquals("@Writer ", Mentions.insert("@", "Writer"))
        assertEquals("line one\n@Writer ", Mentions.insert("line one\n@", "Writer"))
        // the text before the @ is untouched, and an @ inside an email stays
        assertEquals("mail amina@example.com then @Writer ", Mentions.insert("mail amina@example.com then @Wr", "Writer"))
    }

    @Test fun anAgentIsDescribedByTheFirstSentenceOfItsInstructions() {
        assertEquals("Careful reviewer of pull requests.", agentPurpose("You are a careful reviewer of pull requests. Always be kind.\nSecond line."))
        assertEquals("Keeps my inbox tidy.", agentPurpose("\n  Keeps my inbox tidy. And more."))
        assertEquals("Research partner", agentPurpose("you are the research partner"))
        assertEquals("", agentPurpose("   "))
        val long = agentPurpose("A".repeat(300))
        assertEquals(90, long.length)
        assertTrue(long.endsWith("…"))
    }

    @Test fun anAgentIsWorkingOnlyWhenACardGivenToItIsRunning() {
        val cards = listOf(
            CardDto(id = "1", column = "running", agent = "Researcher"),
            CardDto(id = "2", column = "ready", agent = "Writer"),
            CardDto(id = "3", column = "review", agent = "Reviewer"),
            CardDto(id = "4", column = "running", agent = ""),
            CardDto(id = "5", column = "running", agent = "main"),
        )
        assertEquals(setOf("researcher", "main"), workingAgents(cards))
        assertEquals(emptySet<String>(), workingAgents(emptyList()))
    }

    @Test fun theBoardWidgetCountsReadyRunningAndReview() {
        val s = BoardWidgetState.of(
            listOf("backlog", "ready", "ready", "running", "review", "review", "review", "done").mapIndexed { i, c -> CardDto(id = "$i", column = c) },
        )
        assertEquals(Triple(2, 1, 3), Triple(s.ready, s.running, s.review))
        assertTrue(s.known && s.available && s.signedIn)
        assertNull(boardWidgetNote(s))
    }

    @Test fun theBoardWidgetSurvivesARestartAndDegradesQuietly() {
        val s = BoardWidgetState(true, known = true, ready = 4, running = 2, review = 0)
        assertEquals("4,2,0", s.encode())
        assertEquals(s, BoardWidgetState.decode(true, "4,2,0"))
        assertEquals("off", BoardWidgetState(true, known = true, available = false).encode())
        assertEquals("No board on this server.", boardWidgetNote(BoardWidgetState.decode(true, "off")))
        assertEquals("Open Talaria to sign in.", boardWidgetNote(BoardWidgetState.decode(false, "4,2,0")))
        // nothing seen yet, or something unreadable: no numbers are invented
        assertEquals("Open the board to see your cards.", boardWidgetNote(BoardWidgetState.decode(true, "")))
        assertEquals("Open the board to see your cards.", boardWidgetNote(BoardWidgetState.decode(true, "1,2")))
        assertEquals("Open the board to see your cards.", boardWidgetNote(BoardWidgetState.decode(true, "a,b,c")))
        assertEquals(0, BoardWidgetState.decode(true, "-3,2,1").ready)
    }

    @Test fun theBoardCanBeOpenedFromOutsideTheApp() {
        assertEquals(QuickAction.Board, QuickAction.from("app.talaria.action.BOARD"))
    }
}
