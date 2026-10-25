package com.callscreen.core.telephony.prefilter

/**
 * A phone number in E.164 form (`+` then 7 to 15 digits, no leading zero):
 * the shared kernel's `PhoneNumber`, "normalised, equality on the normalised
 * form" (docs/domain/11-consumer.md). The same rule identity applies to an
 * MSISDN, so a number is equal here exactly when it is equal there.
 */
@JvmInline
public value class E164Number private constructor(public val value: String) {
    override fun toString(): String = value

    public companion object {
        private val E164 = Regex("^\\+[1-9][0-9]{6,14}$")

        /** [value] if it is already E.164, else null. */
        public fun of(value: String): E164Number? = if (E164.matches(value)) E164Number(value) else null
    }
}

/**
 * Normalises a number as the handset receives or stores it to E.164, for an
 * Indian subscriber (the product is India-first; ADR-0002 §2).
 *
 * Deliberately conservative: a form whose meaning is not certain gives null,
 * never a guess. The pre-filter screens a number it cannot normalise, so an
 * unrecognised form costs a screening, never a wanted call (the degradation
 * direction in docs/domain/11-consumer.md §11.15). A guess could match a
 * different number's block and reject a call the subscriber wanted.
 *
 * Accepted, after removing spaces, hyphens, dots and parentheses:
 * - `+` and an E.164 number, for any country;
 * - `00` and an E.164 number without the `+` (India's international prefix);
 * - an Indian national number: ten digits starting 6 to 9 (mobile), or a
 *   trunk `0` followed by ten digits starting 1 to 9 (mobile or landline with
 *   its area code), or `91` followed by a ten-digit mobile number.
 *
 * Everything else gives null: short codes (including emergency numbers such as
 * 112, which therefore can never match a block), local landline numbers
 * without an area code, letters, and empty input.
 */
public object PhoneNumbers {
    private const val INDIA = "+91"
    private val separators = Regex("[\\s().-]")
    private val digits = Regex("^[0-9]+$")
    private val indianMobile = Regex("^[6-9][0-9]{9}$")
    private val indianNational = Regex("^[1-9][0-9]{9}$")

    public fun normalise(raw: String): E164Number? {
        val s = raw.replace(separators, "")
        return when {
            s.startsWith("+") -> E164Number.of(s)
            !digits.matches(s) -> null
            s.startsWith("00") -> E164Number.of("+" + s.substring(2))
            indianMobile.matches(s) -> E164Number.of(INDIA + s)
            s.length == 11 && s[0] == '0' && indianNational.matches(s.substring(1)) ->
                E164Number.of(INDIA + s.substring(1))
            s.length == 12 && s.startsWith("91") && indianMobile.matches(s.substring(2)) ->
                E164Number.of(INDIA + s.substring(2))
            else -> null
        }
    }
}
