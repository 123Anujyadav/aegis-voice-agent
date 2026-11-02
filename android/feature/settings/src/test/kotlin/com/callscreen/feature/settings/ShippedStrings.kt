package com.callscreen.feature.settings

import java.io.File
import javax.xml.parsers.DocumentBuilderFactory
import org.w3c.dom.Element

/**
 * The module's words as they ship, for tests that run on the JVM without
 * Android: every res/values* strings.xml (English and each translation),
 * read as Android reads them, with its escapes undone.
 */
internal object ShippedStrings {

    /** Unit tests run in the module directory (android/feature/settings). */
    private val res = File("src/main/res")

    /** Resource folder (values, values-hi, …) to its strings, by name. */
    val byFolder: Map<String, Map<String, String>> by lazy {
        val files = res.listFiles { f -> f.isDirectory && f.name.startsWith("values") }.orEmpty()
            .map { it.name to File(it, "strings.xml") }
            .filter { (_, file) -> file.isFile }
        check(files.any { (folder, _) -> folder == "values" }) { "no English strings.xml under ${res.absolutePath}" }
        files.associate { (folder, file) ->
            val nodes = DocumentBuilderFactory.newInstance().newDocumentBuilder().parse(file).getElementsByTagName("string")
            folder to (0 until nodes.length).associate { i ->
                val e = nodes.item(i) as Element
                e.getAttribute("name") to e.textContent.replace("\\'", "'").replace("\\\"", "\"")
            }
        }
    }

    /** The resource name of a string id. */
    fun name(id: Int): String = R.string::class.java.fields.single { it.getInt(null) == id }.name
}
