package com.callscreen.core.designsystem.token

import org.junit.jupiter.api.Assertions.assertEquals
import org.junit.jupiter.api.Assertions.assertTrue
import org.junit.jupiter.api.DisplayName
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.assertAll

/**
 * Structural invariants of the spacing, radius, icon and motion scales.
 *
 * These do not test rendering — they test that the scales remain *scales*. A
 * value inserted out of order, or a step that stops being a multiple of the base
 * unit, quietly destroys the rhythm the whole system depends on.
 */
@DisplayName("Token scales")
class TokenScaleTest {

    @Test
    @DisplayName("the spacing scale is strictly ascending")
    fun spacingAscends() {
        val scale = listOf(
            Spacing.none, Spacing.hairline, Spacing.xs, Spacing.sm, Spacing.md,
            Spacing.lg, Spacing.xl, Spacing.xxl, Spacing.xxxl, Spacing.huge,
        )
        assertAll(
            scale.zipWithNext().mapIndexed { index, (a, b) ->
                {
                    assertTrue(b > a) {
                        "spacing step $index is not ascending: $a then $b"
                    }
                }
            },
        )
    }

    @Test
    @DisplayName("every spacing value above hairline is a multiple of 4dp")
    fun spacingFollowsFourPointGrid() {
        // The 4dp baseline grid is what keeps vertical rhythm consistent across
        // independently-built screens. `hairline` is the one documented
        // exception and exists only for optical icon alignment in dense chips.
        val gridValues = listOf(
            Spacing.xs, Spacing.sm, Spacing.md, Spacing.lg,
            Spacing.xl, Spacing.xxl, Spacing.xxxl, Spacing.huge,
        )
        assertAll(
            gridValues.map { value ->
                {
                    assertEquals(0f, value.value % BASE_UNIT_DP) {
                        "$value is not a multiple of ${BASE_UNIT_DP}dp"
                    }
                }
            },
        )
    }

    @Test
    @DisplayName("the minimum touch target is 48dp")
    fun minimumTouchTargetIsFortyEight() {
        // Non-negotiable, and lower than this fails motor accessibility
        // regardless of how large the component looks.
        assertEquals(TOUCH_TARGET_DP, Spacing.minTouchTarget.value)
    }

    @Test
    @DisplayName("the radius scale is strictly ascending")
    fun radiusAscends() {
        val scale = listOf(
            Radius.none, Radius.xs, Radius.sm, Radius.md,
            Radius.lg, Radius.xl, Radius.xxl, Radius.full,
        )
        assertAll(
            scale.zipWithNext().mapIndexed { index, (a, b) ->
                { assertTrue(b > a) { "radius step $index is not ascending: $a then $b" } }
            },
        )
    }

    @Test
    @DisplayName("icon sizes ascend and stroke weight never decreases with size")
    fun iconScaleIsCoherent() {
        // Stroke is redrawn per size rather than scaled. A larger icon with a
        // thinner stroke would look like a different family.
        val sizes = IconSize.entries
        assertAll(
            sizes.zipWithNext().map { (a, b) ->
                {
                    assertTrue(b.size > a.size) { "${b.name} is not larger than ${a.name}" }
                    assertTrue(b.stroke >= a.stroke) {
                        "${b.name} has a thinner stroke than ${a.name}"
                    }
                }
            },
        )
    }

    @Test
    @DisplayName("elevation levels ascend in both dp and tonal step")
    fun elevationAscends() {
        val levels = Elevation.entries
        assertAll(
            levels.zipWithNext().map { (a, b) ->
                {
                    assertTrue(b.dp >= a.dp) { "${b.name} is not raised above ${a.name}" }
                    assertTrue(b.tonalStep >= a.tonalStep) {
                        "${b.name} has a lower tonal step than ${a.name}"
                    }
                }
            },
        )
    }

    @Test
    @DisplayName("shadow offset is always positive so light falls from above")
    fun shadowsHaveDownwardOffset() {
        // A symmetric shadow is a glow, and glows belong to a different design
        // language than this one.
        val withShadow = ShadowToken.entries.filter { it != ShadowToken.None }
        assertAll(
            withShadow.map { token ->
                {
                    assertTrue(token.offsetY.value > 0f) {
                        "${token.name} has no downward offset"
                    }
                }
            },
        )
    }

    @Test
    @DisplayName("larger shadows are softer but not more opaque than is legible")
    fun shadowOpacityStaysSubtle() {
        assertAll(
            ShadowToken.entries.map { token ->
                {
                    assertTrue(token.alpha <= MAX_SHADOW_ALPHA) {
                        "${token.name} at ${token.alpha} is too opaque — shadows read as holes above $MAX_SHADOW_ALPHA"
                    }
                }
            },
        )
    }

    @Test
    @DisplayName("motion durations ascend through the scale")
    fun motionDurationsAscend() {
        val scale = listOf(
            Motion.Duration.INSTANT,
            Motion.Duration.MICRO,
            Motion.Duration.SHORT,
            Motion.Duration.MEDIUM,
            Motion.Duration.LONG,
            Motion.Duration.SLOW,
        )
        assertAll(
            scale.zipWithNext().mapIndexed { index, (a, b) ->
                { assertTrue(b > a) { "duration step $index is not ascending: $a then $b" } }
            },
        )
    }

    @Test
    @DisplayName("the recording pulse stays below the WCAG flash threshold")
    fun recordingPulseIsSafe() {
        // WCAG 2.3.1 — nothing may flash more than three times per second.
        assertTrue(Motion.Voice.RECORDING_PULSE_HZ < WCAG_FLASH_THRESHOLD_HZ) {
            "recording pulse at ${Motion.Voice.RECORDING_PULSE_HZ}Hz exceeds the " +
                "${WCAG_FLASH_THRESHOLD_HZ}Hz WCAG 2.3.1 threshold"
        }
    }

    @Test
    @DisplayName("list stagger cannot make the last item feel slow")
    fun staggerStaysUnderPerceptibleDelay() {
        // Beyond ~100ms of accumulated delay the list reads as slow to appear
        // rather than pleasingly sequenced.
        val worstCase = Motion.Interaction.LIST_STAGGER_MS *
            Motion.Interaction.LIST_STAGGER_MAX_ITEMS
        assertTrue(worstCase <= Motion.Duration.MICRO) {
            "worst-case stagger of ${worstCase}ms exceeds ${Motion.Duration.MICRO}ms"
        }
    }

    @Test
    @DisplayName("the pressed scale is a subtle reduction, never a bounce")
    fun pressedScaleIsSubtle() {
        assertTrue(Motion.Interaction.PRESSED_SCALE in MIN_PRESS_SCALE..<1f) {
            "pressed scale ${Motion.Interaction.PRESSED_SCALE} is outside the subtle range"
        }
    }

    private companion object {
        const val BASE_UNIT_DP = 4f
        const val TOUCH_TARGET_DP = 48f
        const val MAX_SHADOW_ALPHA = 0.15f
        const val WCAG_FLASH_THRESHOLD_HZ = 3
        const val MIN_PRESS_SCALE = 0.9f
    }
}
