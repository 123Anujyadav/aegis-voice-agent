package com.callscreen.app

import android.os.Bundle
import com.callscreen.BuildConfig
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import com.callscreen.app.navigation.AegisDestinations
import com.callscreen.app.navigation.AegisNavGraph
import com.callscreen.app.di.IdentityGraph
import com.callscreen.app.session.SessionSignals
import com.callscreen.core.designsystem.theme.CallScreenTheme
import com.callscreen.core.telephony.screening.PreferencesFlag
import com.callscreen.core.telephony.screening.ScreeningRole
import com.callscreen.core.telephony.screening.ScreeningRoleWatch
import dagger.hilt.android.AndroidEntryPoint
import javax.inject.Inject

@AndroidEntryPoint
public class MainActivity : ComponentActivity() {

    /** The app's one identity flow (IdentityModule); its authenticator is null when sign-in is unavailable. */
    @Inject
    public lateinit var identity: IdentityGraph

    /** The stored session, for the cold-start route and the signed-out notice. Local state only. */
    private val session: SessionSignals by lazy { SessionSignals.of(identity.authenticator) }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val startDestination = startDestinationFor(intent.getStringExtra(START_DESTINATION_EXTRA), BuildConfig.DEBUG)
        setContent {
            CallScreenTheme {
                AegisNavGraph(startDestination = startDestination, session = session)
            }
        }
    }

    /**
     * F14 needs to know the role was ever held. Recorded on every foreground,
     * whichever screen is showing: a subscriber who turns blocking on from
     * Protection and loses the role before opening the Calls feed must still
     * be warned there.
     */
    override fun onResume() {
        super.onResume()
        ScreeningRoleWatch(PreferencesFlag(this, PreferencesFlag.HELD_BEFORE)).observe(ScreeningRole.status(this))
    }

    override fun onNewIntent(intent: android.content.Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        val startDestination = startDestinationFor(intent.getStringExtra(START_DESTINATION_EXTRA), BuildConfig.DEBUG)
        setContent {
            CallScreenTheme {
                AegisNavGraph(startDestination = startDestination, session = session)
            }
        }
    }

    internal companion object {
        /** Debug builds only: open a given screen directly (manual testing). */
        const val START_DESTINATION_EXTRA: String = "startDestination"

        /**
         * Where the app starts. MainActivity is exported (it is the launcher),
         * so any app could send this extra: in a release build it is ignored
         * and the app always starts at the splash screen, so nothing can skip
         * onboarding and sign-in. Debug builds honour it for manual testing.
         */
        fun startDestinationFor(requested: String?, debuggable: Boolean): String =
            if (debuggable && !requested.isNullOrEmpty()) requested else AegisDestinations.SPLASH
    }
}
