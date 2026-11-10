package com.callscreen.app.navigation

import androidx.activity.compose.LocalOnBackPressedDispatcherOwner
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import androidx.navigation.NavBackStackEntry
import androidx.navigation.NavGraph
import androidx.navigation.NavGraphBuilder
import androidx.navigation.NavHostController
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.currentBackStackEntryAsState
import androidx.navigation.compose.rememberNavController
import com.callscreen.BuildConfig
import com.callscreen.app.session.SessionRouting
import com.callscreen.app.session.SessionSignals
import com.callscreen.app.session.SignedOutScreen
import com.callscreen.core.auth.SessionEndCause
import com.callscreen.core.telephony.prefilter.ListType
import com.callscreen.core.telephony.screening.ScreeningRole
import com.callscreen.core.telephony.screening.ScreeningRoleDeclines
import com.callscreen.core.telephony.screening.ScreeningRoleStatus
import com.callscreen.core.ui.components.AegisTab
import com.callscreen.core.ui.components.LocalAegisBack
import com.callscreen.feature.assistant.AssistantHomeScreen
import com.callscreen.feature.assistant.VoiceConversationScreen
import com.callscreen.feature.assistant.VoiceSetupScreen
import com.callscreen.feature.business.AegisOpsDashboardScreen
import com.callscreen.feature.business.AiOpsStudioScreen
import com.callscreen.feature.business.AiReceptionistConfigScreen
import com.callscreen.feature.business.BusinessDashboardScreen
import com.callscreen.feature.business.CallRoutingBuilderScreen
import com.callscreen.feature.business.CallRoutingRulesScreen
import com.callscreen.feature.business.ConversationTraceReplayScreen
import com.callscreen.feature.calls.CallsFeedRoute
import com.callscreen.feature.calls.IncomingCallScreen
import com.callscreen.feature.family.FamilyShieldScreen
import com.callscreen.feature.onboarding.AiScreeningSetupScreen
import com.callscreen.feature.onboarding.CallForwardingSetupScreen
import com.callscreen.feature.onboarding.PhoneVerificationRoute
import com.callscreen.feature.onboarding.ScreeningRoleRoute
import com.callscreen.feature.onboarding.SetupCompleteScreen
import com.callscreen.feature.onboarding.SimDetectionRoute
import com.callscreen.feature.onboarding.SplashScreen
import com.callscreen.feature.onboarding.WelcomeScreen
import com.callscreen.feature.protection.CallerListRoute
import com.callscreen.feature.protection.CallerListViewModel
import com.callscreen.feature.protection.FraudIntelligenceScreen
import com.callscreen.feature.protection.ProtectionDashboardScreen
import com.callscreen.feature.screening.EmergencyDetectionScreen
import com.callscreen.feature.screening.FraudWarningScreen
import com.callscreen.feature.screening.LiveAiScreeningScreen
import com.callscreen.feature.settings.AccountDeletionRoute
import com.callscreen.feature.settings.AppLanguageRoute
import com.callscreen.feature.settings.ConsentVaultScreen
import com.callscreen.feature.settings.TaskCenterRoute
import com.callscreen.feature.summary.ConversationMemoryScreen
import com.callscreen.feature.summary.PostCallSummaryScreen
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

public object AegisDestinations {
    public const val SPLASH: String = "splash"
    public const val WELCOME: String = "welcome"
    public const val SIM_DETECTION: String = "sim_detection"
    public const val PHONE_VERIFICATION: String = "phone_verification"
    public const val SCREENING_ROLE: String = "screening_role"
    public const val AI_SETUP: String = "ai_setup"
    public const val CALL_FORWARDING: String = "call_forwarding"
    public const val SETUP_COMPLETE: String = "setup_complete"
    public const val CALLS_FEED: String = "calls_feed"
    public const val INCOMING_CALL: String = "incoming_call"
    public const val LIVE_SCREENING: String = "live_screening"
    public const val POST_CALL_SUMMARY: String = "post_call_summary"
    public const val FRAUD_WARNING: String = "fraud_warning"
    public const val EMERGENCY_ALERT: String = "emergency_alert"
    public const val PROTECTION_DASHBOARD: String = "protection_dashboard"
    public const val FRAUD_INTELLIGENCE: String = "fraud_intelligence"

    /** A32 or A33; the ViewModel reads the list from the route's argument. */
    public const val CALLER_LIST: String = "caller_list/{${CallerListViewModel.LIST_ARG}}"

    public fun callerList(type: ListType): String = "caller_list/${CallerListViewModel.argOf(type)}"
    public const val FAMILY_SHIELD: String = "family_shield"
    public const val ASSISTANT_HOME: String = "assistant_home"
    public const val VOICE_SETUP: String = "voice_setup"
    public const val TASK_CENTER: String = "task_center"
    public const val ACCOUNT_DELETION: String = "account_deletion"
    /** A58l, the app's language (not the assistant's on calls). */
    public const val APP_LANGUAGE: String = "app_language"
    public const val VOICE_CONVERSATION: String = "voice_conversation"
    public const val CONVERSATION_MEMORY: String = "conversation_memory"
    public const val BUSINESS_RECEPTIONIST_CONFIG: String = "business_receptionist_config"
    public const val BUSINESS_ROUTING_BUILDER: String = "business_routing_builder"
    public const val BUSINESS_AI_OPS_STUDIO: String = "business_ai_ops_studio"
    public const val BUSINESS_DASHBOARD: String = "business_dashboard"
    public const val BUSINESS_CONVERSATION_TRACE: String = "business_conversation_trace"
    public const val BUSINESS_OPS_DASHBOARD: String = "business_ops_dashboard"
    public const val BUSINESS_ROUTING_RULES: String = "business_routing_rules"
    public const val CONSENT_VAULT: String = "consent_vault"

    internal const val SIGNED_OUT_CAUSE_ARG: String = "cause"

    /** A73, the signed-out notice; the argument is a SessionEndCause name. */
    public const val SIGNED_OUT: String = "signed_out/{$SIGNED_OUT_CAUSE_ARG}"

    public fun signedOut(cause: SessionEndCause): String = "signed_out/${cause.name}"

    /** Prototype destinations, registered only in debug builds (AegisNavGraph.includeDesignPreviews). */
    public val DESIGN_PREVIEWS: Set<String> = setOf(
        INCOMING_CALL,
        LIVE_SCREENING,
        POST_CALL_SUMMARY,
        FRAUD_WARNING,
        EMERGENCY_ALERT,
        FRAUD_INTELLIGENCE,
        FAMILY_SHIELD,
        CONVERSATION_MEMORY,
        BUSINESS_RECEPTIONIST_CONFIG,
        BUSINESS_ROUTING_BUILDER,
        BUSINESS_AI_OPS_STUDIO,
        BUSINESS_DASHBOARD,
        BUSINESS_CONVERSATION_TRACE,
        BUSINESS_OPS_DASHBOARD,
        BUSINESS_ROUTING_RULES,
        CONSENT_VAULT,
    )
}

@Composable
public fun AegisNavGraph(
    navController: NavHostController = rememberNavController(),
    startDestination: String = AegisDestinations.SPLASH,
    modifier: Modifier = Modifier,
    /** Debug builds only: the prototype screens, framed as design previews (see [designPreviews]). */
    includeDesignPreviews: Boolean = BuildConfig.DEBUG,
    /** The stored session, for the cold-start route and the signed-out notice. None: no session handling. */
    session: SessionSignals = SessionSignals.None,
) {
    // A session that goes away while the app is in use shows A73, once. The
    // first value collected is the current one, so starting (or recreating
    // the activity) never counts as a change. Navigation stays on the main
    // thread whichever thread the session is read on.
    LaunchedEffect(session, navController) {
        withContext(Dispatchers.Main.immediate) {
            val state = session.state()
            var previous = state.value
            state.collect { current ->
                val cause = SessionRouting.signedOutNotice(previous, current, navController.currentDestination?.route)
                previous = current
                if (cause != null) {
                    navController.startOver(AegisDestinations.signedOut(cause))
                }
            }
        }
    }
    // Once the splash has routed, and each time the app returns to the
    // foreground, the session is confirmed with the server when a check is
    // due (SessionCheck: at most hourly, never on every launch). Never during
    // the splash: a check may refresh the token under the authenticator's
    // lock, which the splash's local read would then wait for.
    val route = navController.currentBackStackEntryAsState().value?.destination?.route
    val pastSplash = route != null && route != AegisDestinations.SPLASH
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    LaunchedEffect(session, pastSplash, lifecycle) {
        if (pastSplash) lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) { session.confirmIfDue() }
    }
    // D6: each screen's top bar gets a Back arrow when the app has a screen behind it, and the
    // arrow is Android's own Back (the dispatcher), so it pops exactly as the system Back does,
    // through any screen's own Back handling, and keeps the state the back stack keeps.
    val backStack by navController.currentBackStack.collectAsState()
    val dispatcher = LocalOnBackPressedDispatcherOwner.current?.onBackPressedDispatcher
    val back: @Composable () -> (() -> Unit)? = remember(dispatcher) {
        @Composable {
            val screen = LocalLifecycleOwner.current as? NavBackStackEntry
            val index = backStack.indexOfFirst { it.id == screen?.id }
            val behind = index > 0 && backStack.subList(0, index).any { it.destination !is NavGraph }
            if (behind && dispatcher != null) ({ dispatcher.onBackPressed() }) else null
        }
    }
    CompositionLocalProvider(LocalAegisBack provides back) {
        NavHost(
            navController = navController,
            startDestination = startDestination,
            modifier = modifier
        ) {
            composable(AegisDestinations.SPLASH) {
                val scope = rememberCoroutineScope()
                SplashScreen(
                    onSplashFinished = {
                        // Calls for a phone holding a session, onboarding otherwise
                        // (02-navigation-graph §2.2); an unreadable store counts as none.
                        scope.launch(Dispatchers.Main.immediate) {
                            val next = SessionRouting.afterSplash(session)
                            navController.navigate(next) { popUpTo(AegisDestinations.SPLASH) { inclusive = true } }
                        }
                    },
                )
            }
            composable(AegisDestinations.WELCOME) {
                WelcomeScreen(
                    onGetStartedClick = { navController.navigate(AegisDestinations.SIM_DETECTION) },
                    onLanguageClick = { navController.navigate(AegisDestinations.APP_LANGUAGE) },
                )
            }
            composable(AegisDestinations.SIM_DETECTION) {
                SimDetectionRoute(onContinueClick = { navController.navigate(AegisDestinations.PHONE_VERIFICATION) })
            }
            composable(AegisDestinations.PHONE_VERIFICATION) {
                val context = LocalContext.current
                PhoneVerificationRoute(
                    onVerified = {
                        // A09b only when the role can be asked for and is not held (A09 edge cases),
                        // and not after a second decline ("permanently denied": ScreeningRoleDeclines).
                        val next = if (ScreeningRole.status(context) == ScreeningRoleStatus.NOT_HELD &&
                            ScreeningRoleDeclines.on(context).shouldOffer()
                        ) {
                            AegisDestinations.SCREENING_ROLE
                        } else {
                            AegisDestinations.AI_SETUP
                        }
                        navController.navigate(next)
                    },
                )
            }
            composable(AegisDestinations.SCREENING_ROLE) {
                ScreeningRoleRoute(onDone = { navController.navigate(AegisDestinations.AI_SETUP) })
            }
            composable(AegisDestinations.AI_SETUP) {
                AiScreeningSetupScreen(onContinueClick = { navController.navigate(AegisDestinations.CALL_FORWARDING) })
            }
            composable(AegisDestinations.CALL_FORWARDING) {
                CallForwardingSetupScreen(onActivateForwardingClick = { navController.navigate(AegisDestinations.SETUP_COMPLETE) })
            }
            composable(AegisDestinations.SETUP_COMPLETE) {
                val context = LocalContext.current
                SetupCompleteScreen(
                    // Onboarding exits permanently: Back from Calls never returns to it.
                    onGoToHomeClick = {
                        navController.startOver(AegisDestinations.CALLS_FEED)
                    },
                    blockingOn = ScreeningRole.status(context) == ScreeningRoleStatus.HELD,
                )
            }
            composable(AegisDestinations.CALLS_FEED) {
                CallsFeedRoute(
                    // A call's summary is a design preview until call records exist; nothing to open in a release build.
                    onCallClick = { if (includeDesignPreviews) navController.navigate(AegisDestinations.POST_CALL_SUMMARY) },
                    onTabSelected = { tab -> navController.selectTab(tab) },
                )
            }
            composable(AegisDestinations.PROTECTION_DASHBOARD) {
                ProtectionDashboardScreen(
                    onTabSelected = { tab -> navController.selectTab(tab) },
                    onBlockedNumbersClick = { navController.navigate(AegisDestinations.callerList(ListType.BLOCK)) },
                    onAllowedNumbersClick = { navController.navigate(AegisDestinations.callerList(ListType.ALLOW)) },
                )
            }
            composable(AegisDestinations.CALLER_LIST) {
                CallerListRoute()
            }
            if (includeDesignPreviews) designPreviews(navController)
            composable(AegisDestinations.ASSISTANT_HOME) {
                AssistantHomeScreen(
                    onVoiceSetupClick = { navController.navigate(AegisDestinations.VOICE_SETUP) },
                    onStartVoiceChatClick = { navController.navigate(AegisDestinations.VOICE_CONVERSATION) },
                    onTabSelected = { tab -> navController.selectTab(tab) }
                )
            }
            composable(AegisDestinations.VOICE_SETUP) {
                VoiceSetupScreen(onSaveVoiceClick = { navController.popBackStack() })
            }
            composable(AegisDestinations.TASK_CENTER) {
                TaskCenterRoute(
                    onLanguageClick = { navController.navigate(AegisDestinations.APP_LANGUAGE) },
                    onDeleteAccountClick = { navController.navigate(AegisDestinations.ACCOUNT_DELETION) },
                    onTabSelected = { tab -> navController.selectTab(tab) },
                )
            }
            composable(AegisDestinations.APP_LANGUAGE) {
                AppLanguageRoute()
            }
            composable(AegisDestinations.ACCOUNT_DELETION) {
                AccountDeletionRoute(
                    // The account is gone: nothing behind this screen applies any more.
                    onDeleted = {
                        navController.startOver(AegisDestinations.WELCOME)
                    },
                    onBack = { navController.popBackStack() },
                )
            }
            composable(AegisDestinations.VOICE_CONVERSATION) {
                VoiceConversationScreen(onEndSessionClick = { navController.popBackStack() })
            }
            composable(AegisDestinations.SIGNED_OUT) { entry ->
                val context = LocalContext.current
                val name = entry.arguments?.getString(AegisDestinations.SIGNED_OUT_CAUSE_ARG)
                SignedOutScreen(
                    cause = SessionEndCause.entries.firstOrNull { it.name == name },
                    blockingOn = ScreeningRole.status(context) == ScreeningRoleStatus.HELD,
                    onSignInClick = {
                        navController.startOver(AegisDestinations.WELCOME)
                    },
                )
            }
        }
    }
}

/**
 * The prototype screens: every value on them is sample data, and none is
 * backed by an API or stored data (docs/phase19/PROTOTYPE-SCREENS-AUDIT.md).
 * Nothing in the app navigates to them except one another, so they are
 * registered only in debug builds, each under a visible "design preview"
 * label. A release build cannot open them.
 */
private fun NavGraphBuilder.designPreviews(navController: NavHostController) {
    composable(AegisDestinations.INCOMING_CALL) {
        DesignPreview {
            IncomingCallScreen(
                onScreenWithAiClick = { navController.navigate(AegisDestinations.LIVE_SCREENING) },
                onAnswerClick = {},
                onDeclineClick = { navController.popBackStack() }
            )
        }
    }
    composable(AegisDestinations.LIVE_SCREENING) {
        DesignPreview {
            LiveAiScreeningScreen(
                onTakeOverClick = {},
                onEndCallClick = { navController.navigate(AegisDestinations.POST_CALL_SUMMARY) }
            )
        }
    }
    composable(AegisDestinations.POST_CALL_SUMMARY) {
        DesignPreview {
            PostCallSummaryScreen(
                onSaveContactClick = { navController.navigate(AegisDestinations.CALLS_FEED) },
                onBlockReportClick = { navController.navigate(AegisDestinations.CALLS_FEED) }
            )
        }
    }
    composable(AegisDestinations.FRAUD_WARNING) {
        DesignPreview {
            FraudWarningScreen(onTerminateAndBlockClick = { navController.navigate(AegisDestinations.CALLS_FEED) })
        }
    }
    composable(AegisDestinations.EMERGENCY_ALERT) {
        DesignPreview {
            EmergencyDetectionScreen(onConnectImmediatelyClick = { navController.navigate(AegisDestinations.CALLS_FEED) })
        }
    }
    composable(AegisDestinations.FRAUD_INTELLIGENCE) {
        DesignPreview {
            FraudIntelligenceScreen()
        }
    }
    composable(AegisDestinations.FAMILY_SHIELD) {
        DesignPreview {
            FamilyShieldScreen()
        }
    }
    composable(AegisDestinations.CONVERSATION_MEMORY) {
        DesignPreview {
            ConversationMemoryScreen()
        }
    }
    composable(AegisDestinations.BUSINESS_RECEPTIONIST_CONFIG) {
        DesignPreview {
            AiReceptionistConfigScreen(onBackClick = { navController.popBackStack() })
        }
    }
    composable(AegisDestinations.BUSINESS_ROUTING_BUILDER) {
        DesignPreview {
            CallRoutingBuilderScreen(onBackClick = { navController.popBackStack() })
        }
    }
    composable(AegisDestinations.BUSINESS_AI_OPS_STUDIO) {
        DesignPreview {
            AiOpsStudioScreen(onBackClick = { navController.popBackStack() })
        }
    }
    composable(AegisDestinations.BUSINESS_DASHBOARD) {
        DesignPreview {
            BusinessDashboardScreen(
                onBackClick = { navController.popBackStack() },
                onConfigureClick = { navController.navigate(AegisDestinations.BUSINESS_RECEPTIONIST_CONFIG) }
            )
        }
    }
    composable(AegisDestinations.BUSINESS_CONVERSATION_TRACE) {
        DesignPreview {
            ConversationTraceReplayScreen(onBackClick = { navController.popBackStack() })
        }
    }
    composable(AegisDestinations.BUSINESS_OPS_DASHBOARD) {
        DesignPreview {
            AegisOpsDashboardScreen(onBackClick = { navController.popBackStack() })
        }
    }
    composable(AegisDestinations.BUSINESS_ROUTING_RULES) {
        DesignPreview {
            CallRoutingRulesScreen(onBackClick = { navController.popBackStack() })
        }
    }
    composable(AegisDestinations.CONSENT_VAULT) {
        DesignPreview {
            ConsentVaultScreen(onBackClick = { navController.popBackStack() })
        }
    }
}

/** Each tab's root route (02 §2.1). */
private val TAB_ROOTS: Map<AegisTab, String> = mapOf(
    AegisTab.Calls to AegisDestinations.CALLS_FEED,
    AegisTab.Protection to AegisDestinations.PROTECTION_DASHBOARD,
    AegisTab.Assistant to AegisDestinations.ASSISTANT_HOME,
    AegisTab.Settings to AegisDestinations.TASK_CENTER,
)

/**
 * Shows [tab] as the navigation spec has it (02 §2.1, §2.4, §2.6; F25, owner
 * 2026-11-05). Each tab keeps its own back stack: the tab being left is saved
 * and popped, the chosen one restored, so only one tab is ever on the back
 * stack, at its bottom. Back on a tab root therefore leaves the app, never
 * cycles through tabs, and the top bar shows no Back arrow there (D6, which
 * mirrors Back). Choosing the tab already shown never navigates (the bar
 * scrolls instead), so the history holds no duplicates.
 */
internal fun NavHostController.selectTab(tab: AegisTab) {
    val target = TAB_ROOTS.getValue(tab)
    if (currentDestination?.route == target) return
    val shown = currentBackStack.value.firstOrNull { it.destination.route in TAB_ROOTS.values }?.destination?.route
    navigate(target) {
        // Pops everything from the shown tab's root up, saving it to restore when its tab is chosen again.
        if (shown != null) {
            popUpTo(shown) {
                inclusive = true
                saveState = true
            }
        }
        launchSingleTop = true
        restoreState = true
    }
}

/**
 * Clears the back stack and every tab's saved stack, then shows [route]: after
 * sign-in, a sign-out or an account deletion, nothing from before may be
 * restored by choosing a tab.
 */
internal fun NavHostController.startOver(route: String) {
    TAB_ROOTS.values.forEach { clearBackStack(it) }
    navigate(route) { popUpTo(graph.id) { inclusive = true } }
}
