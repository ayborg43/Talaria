package app.talaria.voice

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class VadTest {
    private val quiet = 0.004f
    private val talking = 0.12f

    /** A detector that has already listened to a quiet room for a moment, as it does when a conversation starts. */
    private fun settled() = Vad().also { v -> repeat(Vad.CALIBRATION_FRAMES) { v.feed(quiet, false, 4500) } }

    private fun Vad.run(rms: Float, frames: Int, speaking: Boolean = false, max: Int = 4500): List<Vad.Event> = List(frames) { feed(rms, speaking, max) }

    @Test fun silence_never_starts_and_the_floor_follows_the_room() {
        val v = settled()
        assertTrue(v.run(quiet, 500).all { it == Vad.Event.None })
        assertFalse(v.hearing)
        assertTrue(v.noise < 0.006f)
    }

    @Test fun speech_starts_after_three_frames_and_ends_after_a_pause() {
        val v = settled()
        val start = v.run(talking, 3)
        assertEquals(Vad.Event.Started, start.last())
        assertTrue(v.hearing)
        v.run(talking, 80)
        val tail = v.run(quiet, Vad.END_FRAMES)
        assertEquals(Vad.Event.Ended(true), tail.last())
        assertFalse(v.hearing)
    }

    @Test fun a_click_is_not_a_turn() {
        val v = settled()
        v.run(talking, 3)
        val tail = v.run(quiet, Vad.END_FRAMES)
        assertEquals(Vad.Event.Ended(false), tail.last())
    }

    @Test fun a_short_pause_inside_a_sentence_does_not_end_it() {
        val v = settled()
        v.run(talking, 30)
        val pause = v.run(quiet, 30) // 600 ms
        assertTrue(pause.all { it == Vad.Event.None })
        assertTrue(v.hearing)
        v.run(talking, 30)
        assertEquals(Vad.Event.Ended(true), v.run(quiet, Vad.END_FRAMES).last())
    }

    @Test fun the_phones_own_voice_does_not_interrupt_itself() {
        val v = settled()
        // Speech leaking back into the microphone at a level that would start a turn when the phone is quiet.
        assertTrue(v.run(0.05f, 200, speaking = true).all { it == Vad.Event.None })
        assertFalse(v.hearing)
    }

    @Test fun real_speech_over_the_phones_voice_interrupts_it() {
        val v = settled()
        val ev = v.run(0.2f, Vad.INTERRUPT_FRAMES, speaking = true)
        assertEquals(Vad.Event.Started, ev.last())
        assertTrue(ev.dropLast(1).all { it == Vad.Event.None })
    }

    @Test fun a_very_long_turn_is_cut_off() {
        val v = settled()
        v.run(talking, 3, max = 100)
        val ev = v.run(talking, 150, max = 100)
        assertTrue(ev.any { it is Vad.Event.Ended })
    }

    @Test fun a_noisy_room_is_learned_and_not_taken_for_speech() {
        val v = Vad()
        val ev = v.run(0.03f, 600) // a steady fan or traffic from the very start
        assertTrue(ev.all { it == Vad.Event.None })
        assertFalse(v.hearing)
        assertTrue(v.threshold(false) > 0.05f)
    }

    @Test fun speech_over_a_noisy_room_is_still_heard() {
        val v = Vad()
        v.run(0.03f, Vad.CALIBRATION_FRAMES)
        assertEquals(Vad.Event.Started, v.run(0.3f, 3).last())
    }

    @Test fun starting_to_talk_straight_away_does_not_blind_it() {
        val v = Vad()
        v.run(quiet, 22)              // the first part of the calibration is quiet
        v.run(talking, 8)             // then the person starts to talk
        val ev = v.run(talking, 3)    // the floor was taken from the quiet end, so this still counts
        assertEquals(Vad.Event.Started, ev.last())
    }
}
