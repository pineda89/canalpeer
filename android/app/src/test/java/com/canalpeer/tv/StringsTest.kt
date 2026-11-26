package com.canalpeer.tv

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

// Every language has every text, with the same placeholders as English: a missing
// one shows in English, a wrong one crashes getString.
class StringsTest {
    private val res = File("src/main/res")
    private val placeholder = Regex("""%(\d+\$)?[ds]""")

    private fun strings(dir: String): Map<String, String> =
        Regex("""<string name="([^"]+)">(.*?)</string>""", RegexOption.DOT_MATCHES_ALL)
            .findAll(File(res, "$dir/strings.xml").readText())
            .associate { it.groupValues[1] to it.groupValues[2] }

    @Test fun everyLanguageIsComplete() {
        val english = strings("values")
        val languages = listOf("es", "fr", "de", "it", "pt")
        for (lang in languages) {
            val t = strings("values-$lang")
            assertEquals("values-$lang keys", english.keys.sorted(), t.keys.sorted())
            for ((k, v) in english) {
                assertEquals("values-$lang/$k placeholders",
                    placeholder.findAll(v).map { it.value }.sorted().toList(),
                    placeholder.findAll(t.getValue(k)).map { it.value }.sorted().toList())
                // aapt rejects a bare apostrophe.
                assertTrue("values-$lang/$k unescaped '", !Regex("""(?<!\\)'""").containsMatchIn(t.getValue(k)))
            }
        }
    }
}
