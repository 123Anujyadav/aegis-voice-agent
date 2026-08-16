package com.callscreen.core.designsystem.token

import androidx.compose.animation.core.CubicBezierEasing
import androidx.compose.animation.core.Easing
import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.Spring
import androidx.compose.animation.core.SpringSpec
import androidx.compose.animation.core.spring
import androidx.compose.runtime.Immutable

/**
 * Motion tokens.
 *
 * ## The constraint
 *
 * 60 Hz on a mid-range Android GPU — 16.6 ms per frame. That is the target
 * device, not the fallback, because a dropped frame reads as broken software and
 * broken software in a trust product is disproportionately damaging.
 *
 * Three rules follow, and they are not negotiable:
 *
 * - **Transform and opacity only.** They are the only properties that avoid
 *   layout and paint.
 * - **Never animate shadow, blur or colour** on the hot path.
 * - **Never crossfade a large surface** — full-screen alpha forces an offscreen
 *   buffer. Use a shared-axis slide.
 *
 * ## Reduced motion
 *
 * The Android signal is `Settings.Global.ANIMATOR_DURATION_SCALE == 0`, set by
 * *Settings → Accessibility → Remove animations*. It is **not**
 * `prefers-reduced-motion`, which is a web media query that does not exist here.
 * Reading the wrong signal is the most common way this gets missed.
 *
 * @see docs/design/05-motion.md
 */
@Immutable
public object Motion {

    /** Animation durations in milliseconds. */
    public object Duration {

        /** 0 ms. What every duration resolves to under reduced motion. */
        public const val INSTANT: Int = 0

        /** 100 ms. State change — hover, press, checkbox. */
        public const val MICRO: Int = 100

        /** 150 ms. Small element enter and exit — tooltip, badge. */
        public const val SHORT: Int = 150

        /** 250 ms. **The default.** Sheets, dialogs, expansion. */
        public const val MEDIUM: Int = 250

        /** 400 ms. Page transitions and complex reveals. */
        public const val LONG: Int = 400

        /** 600 ms. Onboarding and deliberate emphasis. Rare. */
        public const val SLOW: Int = 600

        /** 1400 ms. One voice-pulse cycle. */
        public const val PULSE: Int = 1400

        /** 1200 ms. One skeleton shimmer sweep. */
        public const val SHIMMER: Int = 1200
    }

    /**
     * Easing curves.
     *
     * Duration scales with **distance, not importance**. An element travelling
     * further needs longer to feel like it has mass; the same duration across
     * different distances makes short moves sluggish and long moves snapped.
     *
     * Exits are one step faster than entrances — the user has already decided,
     * and waiting for a dismissal to finish is friction.
     */
    public object Easings {

        /** The default for on-screen movement. */
        public val standard: Easing = CubicBezierEasing(0.2f, 0f, 0f, 1f)

        /** Entering — fast in, soft settle. */
        public val decelerate: Easing = CubicBezierEasing(0f, 0f, 0f, 1f)

        /** Exiting — leaves decisively. */
        public val accelerate: Easing = CubicBezierEasing(0.3f, 0f, 1f, 1f)

        /** Hero moments and large surfaces. */
        public val emphasized: Easing = CubicBezierEasing(0.2f, 0f, 0f, 1f)

        /**
         * Constant rate. **Reserved** for things that genuinely progress
         * linearly: determinate progress, waveform scroll, the recording timer.
         *
         * Nothing physical starts and stops instantaneously, so using this on
         * movement makes it read as mechanical.
         */
        public val linear: Easing = LinearEasing
    }

    /**
     * Spring specifications for **interruptible** motion.
     *
     * Anything the user can grab mid-flight — sheets, drag, swipe-to-dismiss —
     * uses a spring rather than a duration curve. A spring can be interrupted
     * and redirected without a discontinuity; a duration curve snaps.
     *
     * No spring here overshoots by more than 2%. Bouncy motion reads as playful,
     * and this product is not playful.
     */
    public object Springs {

        /** Sheets and large surfaces. */
        public fun <T> gentle(): SpringSpec<T> = spring(
            dampingRatio = DAMPING_GENTLE,
            stiffness = STIFFNESS_GENTLE,
        )

        /** Standard interactive elements. */
        public fun <T> default(): SpringSpec<T> = spring(
            dampingRatio = Spring.DampingRatioNoBouncy,
            stiffness = Spring.StiffnessMedium,
        )

        /** Small controls and toggles. */
        public fun <T> snappy(): SpringSpec<T> = spring(
            dampingRatio = DAMPING_SNAPPY,
            stiffness = STIFFNESS_SNAPPY,
        )

        private const val DAMPING_GENTLE = 0.9f
        private const val STIFFNESS_GENTLE = 300f
        private const val DAMPING_SNAPPY = 0.7f
        private const val STIFFNESS_SNAPPY = 900f
    }

    /** Values for the standard micro-interactions. */
    public object Interaction {

        /**
         * Scale applied while pressed.
         *
         * Press uses **scale, not elevation** — animating elevation animates a
         * shadow, which is prohibited. A 3% reduction reads as depression more
         * reliably anyway, and costs nothing.
         */
        public const val PRESSED_SCALE: Float = 0.97f

        /** Milliseconds between staggered list items. */
        public const val LIST_STAGGER_MS: Int = 20

        /**
         * Maximum number of items to stagger.
         *
         * Beyond this the last item's delay exceeds 100 ms and the list feels
         * slow to appear rather than pleasingly sequenced.
         */
        public const val LIST_STAGGER_MAX_ITEMS: Int = 5
    }

    /**
     * Voice and call animation constants.
     *
     * ## The honesty rule
     *
     * The voice orb animates from **real amplitude data or not at all**. A
     * synthetic listening pulse rendered while the microphone is closed is a lie
     * told by the interface, and it is exactly what the "never overclaim"
     * principle exists to prevent. When amplitude is unavailable the orb renders
     * static — never fake.
     */
    public object Voice {

        /** Orb scale at zero amplitude. */
        public const val ORB_SCALE_MIN: Float = 1.0f

        /** Orb scale at full amplitude. */
        public const val ORB_SCALE_MAX: Float = 1.08f

        /** Bars in a full-width waveform. */
        public const val WAVEFORM_BARS_FULL: Int = 48

        /** Bars in a compact waveform. */
        public const val WAVEFORM_BARS_COMPACT: Int = 24

        /**
         * Amplitude sampling rate in hertz.
         *
         * Audio callbacks arrive faster than a display can show. Sampling to
         * 30 Hz and interpolating to refresh avoids rendering work that cannot
         * be seen.
         */
        public const val WAVEFORM_SAMPLE_HZ: Int = 30

        /**
         * Recording indicator pulse rate in hertz.
         *
         * 1 Hz, comfortably below the 3 Hz flash threshold in WCAG 2.3.1.
         */
        public const val RECORDING_PULSE_HZ: Int = 1
    }
}
