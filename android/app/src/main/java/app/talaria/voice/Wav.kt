package app.talaria.voice

import java.io.ByteArrayOutputStream
import java.nio.ByteBuffer
import java.nio.ByteOrder

/** 16 kHz mono 16-bit PCM in a WAV: small, and the one format every speech service reads. */
object Wav {
    const val RATE = 16_000

    fun encode(samples: ShortArray): ByteArray {
        val data = samples.size * 2
        val b = ByteBuffer.allocate(44 + data).order(ByteOrder.LITTLE_ENDIAN)
        b.put("RIFF".toByteArray()).putInt(36 + data).put("WAVE".toByteArray())
        b.put("fmt ".toByteArray()).putInt(16).putShort(1).putShort(1).putInt(RATE).putInt(RATE * 2).putShort(2).putShort(16)
        b.put("data".toByteArray()).putInt(data)
        for (s in samples) b.putShort(s)
        return b.array()
    }

    /** A growable list of samples, without boxing each one. */
    class Samples(capacity: Int = RATE * 10) {
        private var a = ShortArray(capacity)
        var size = 0
            private set

        fun add(src: ShortArray, n: Int = src.size) {
            if (size + n > a.size) a = a.copyOf(maxOf(a.size * 2, size + n))
            System.arraycopy(src, 0, a, size, n); size += n
        }

        fun clear() { size = 0 }
        fun toArray(): ShortArray = a.copyOf(size)
    }
}
