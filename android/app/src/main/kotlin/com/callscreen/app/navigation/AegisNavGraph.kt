package com.callscreen.app.navigation

import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.navigation.NavHostController
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import com.callscreen.core.ui.components.AegisTab
import com.callscreen.feature.assistant.AssistantHomeScreen
import com.callscreen.feature.assistant.VoiceConversationScreen
import com.callscreen.feature.assistant.VoiceSetupScreen
import com.callscreen.feature.calls.CallsFeedScreen
import com.callscreen.feature.calls.IncomingCallScreen
import com.callscreen.feature.family.FamilyShieldScreen
import com.callscreen.feature.onboarding.AiScreeningSetupScreen
import com.callscreen.feature.onboarding.CallForwardingSetupScreen
import com.callscreen.feature.onboarding.PermissionsEducationScreen
import com.callscreen.feature.onboarding.PhoneVerificationScreen
import com.callscreen.feature.onboarding.SetupCompleteScreen
import com.callscreen.feature.onboarding.SimDetectionScreen
import com.callscreen.feature.onboarding.SplashScreen
import com.callscreen.feature.onboarding.WelcomeScreen
import com.callscreen.feature.protection.FraudIntelligenceScreen
import com.callscreen.feature.protection.ProtectionDashboardScreen
import com.callscreen.feature.screening.EmergencyDetectionScreen
import com.callscreen.feature.screening.FraudWarningScreen
import com.callscreen.feature.screening.LiveAiScreeningScreen
import com.callscreen.feature.settings.TaskCenterScreen
import com.callscreen.feature.summary.ConversationMemoryScreen
import com.callscreen.feature.summary.PostCallSummaryScreen
import com.callscreen.feature.business.AiReceptionistConfigScreen
import com.callscreen.feature.business.CallRoutingBuilderScreen
import com.callscreen.feature.business.AiOpsStudioScreen
import com.callscreen.feature.business.BusinessDashboardScreen
import com.callscreen.feature.business.ConversationTraceReplayScreen
import com.callscreen.feature.business.AegisOpsDashboardScreen
import com.callscreen.feature.business.CallRoutingRulesScreen
import com.callscreen.feature.settings.ConsentVaultScreen


public object AegisDestinations {
    public const val SPLASH: String = "splash"
    public const val WELCOME: String = "welcome"
    public const val SIM_DETECTION: String = "sim_detection"
    public const val PHONE_VERIFICATION: String = "phone_verification"
    public const val PERMISSIONS: String = "permissions"
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
    public const val FAMILY_SHIELD: String = "family_shield"
    public const val ASSISTANT_HOME: String = "assistant_home"
    public const val VOICE_SETUP: String = "voice_setup"
    public const val TASK_CENTER: String = "task_center"
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
}



@Composable
public fun AegisNavGraph(
    navController: NavHostController = rememberNavController(),
    startDestination: String = AegisDestinations.SPLASH,
    modifier: Modifier = Modifier
) {
    NavHost(
        navController = navController,
        startDestination = startDestination,
        modifier = modifier
    ) {
        composable(AegisDestinations.SPLASH) {
            SplashScreen(onSplashFinished = { navController.navigate(AegisDestinations.WELCOME) })
        }
        composable(AegisDestinations.WELCOME) {
            WelcomeScreen(onGetStartedClick = { navController.navigate(AegisDestinations.SIM_DETECTION) })
        }
        composable(AegisDestinations.SIM_DETECTION) {
            SimDetectionScreen(onContinueClick = { navController.navigate(AegisDestinations.PHONE_VERIFICATION) })
        }
        composable(AegisDestinations.PHONE_VERIFICATION) {
            PhoneVerificationScreen(onSendOtpClick = { navController.navigate(AegisDestinations.PERMISSIONS) })
        }
        composable(AegisDestinations.PERMISSIONS) {
            PermissionsEducationScreen(onGrantPermissionsClick = { navController.navigate(AegisDestinations.AI_SETUP) })
        }
        composable(AegisDestinations.AI_SETUP) {
            AiScreeningSetupScreen(onContinueClick = { navController.navigate(AegisDestinations.CALL_FORWARDING) })
        }
        composable(AegisDestinations.CALL_FORWARDING) {
            CallForwardingSetupScreen(onActivateForwardingClick = { navController.navigate(AegisDestinations.SETUP_COMPLETE) })
        }
        composable(AegisDestinations.SETUP_COMPLETE) {
            SetupCompleteScreen(onGoToHomeClick = { navController.navigate(AegisDestinations.CALLS_FEED) })
        }
        composable(AegisDestinations.CALLS_FEED) {
            CallsFeedScreen(
                onCallClick = { navController.navigate(AegisDestinations.POST_CALL_SUMMARY) },
                onTabSelected = { tab -> handleTabClick(navController, tab) }
            )
        }
        composable(AegisDestinations.INCOMING_CALL) {
            IncomingCallScreen(
                onScreenWithAiClick = { navController.navigate(AegisDestinations.LIVE_SCREENING) },
                onAnswerClick = {},
                onDeclineClick = { navController.popBackStack() }
            )
        }
        composable(AegisDestinations.LIVE_SCREENING) {
            LiveAiScreeningScreen(
                onTakeOverClick = {},
                onEndCallClick = { navController.navigate(AegisDestinations.POST_CALL_SUMMARY) }
            )
        }
        composable(AegisDestinations.POST_CALL_SUMMARY) {
            PostCallSummaryScreen(
                onSaveContactClick = { navController.navigate(AegisDestinations.CALLS_FEED) },
                onBlockReportClick = { navController.navigate(AegisDestinations.CALLS_FEED) }
            )
        }
        composable(AegisDestinations.FRAUD_WARNING) {
            FraudWarningScreen(onTerminateAndBlockClick = { navController.navigate(AegisDestinations.CALLS_FEED) })
        }
        composable(AegisDestinations.EMERGENCY_ALERT) {
            EmergencyDetectionScreen(onConnectImmediatelyClick = { navController.navigate(AegisDestinations.CALLS_FEED) })
        }
        composable(AegisDestinations.PROTECTION_DASHBOARD) {
            ProtectionDashboardScreen(onTabSelected = { tab -> handleTabClick(navController, tab) })
        }
        composable(AegisDestinations.FRAUD_INTELLIGENCE) {
            FraudIntelligenceScreen()
        }
        composable(AegisDestinations.FAMILY_SHIELD) {
            FamilyShieldScreen()
        }
        composable(AegisDestinations.ASSISTANT_HOME) {
            AssistantHomeScreen(
                onVoiceSetupClick = { navController.navigate(AegisDestinations.VOICE_SETUP) },
                onStartVoiceChatClick = { navController.navigate(AegisDestinations.VOICE_CONVERSATION) },
                onTabSelected = { tab -> handleTabClick(navController, tab) }
            )
        }
        composable(AegisDestinations.VOICE_SETUP) {
            VoiceSetupScreen(onSaveVoiceClick = { navController.popBackStack() })
        }
        composable(AegisDestinations.TASK_CENTER) {
            TaskCenterScreen()
        }
        composable(AegisDestinations.VOICE_CONVERSATION) {
            VoiceConversationScreen(onEndSessionClick = { navController.popBackStack() })
        }
        composable(AegisDestinations.CONVERSATION_MEMORY) {
            ConversationMemoryScreen()
        }
        composable(AegisDestinations.BUSINESS_RECEPTIONIST_CONFIG) {
            AiReceptionistConfigScreen(onBackClick = { navController.popBackStack() })
        }
        composable(AegisDestinations.BUSINESS_ROUTING_BUILDER) {
            CallRoutingBuilderScreen(onBackClick = { navController.popBackStack() })
        }
        composable(AegisDestinations.BUSINESS_AI_OPS_STUDIO) {
            AiOpsStudioScreen(onBackClick = { navController.popBackStack() })
        }
        composable(AegisDestinations.BUSINESS_DASHBOARD) {
            BusinessDashboardScreen(
                onBackClick = { navController.popBackStack() },
                onConfigureClick = { navController.navigate(AegisDestinations.BUSINESS_RECEPTIONIST_CONFIG) }
            )
        }
        composable(AegisDestinations.BUSINESS_CONVERSATION_TRACE) {
            ConversationTraceReplayScreen(onBackClick = { navController.popBackStack() })
        }
        composable(AegisDestinations.BUSINESS_OPS_DASHBOARD) {
            AegisOpsDashboardScreen(onBackClick = { navController.popBackStack() })
        }
        composable(AegisDestinations.BUSINESS_ROUTING_RULES) {
            CallRoutingRulesScreen(onBackClick = { navController.popBackStack() })
        }
        composable(AegisDestinations.CONSENT_VAULT) {
            ConsentVaultScreen(onBackClick = { navController.popBackStack() })
        }
    }
}



private fun handleTabClick(navController: NavHostController, tab: AegisTab) {
    when (tab) {
        AegisTab.Calls -> navController.navigate(AegisDestinations.CALLS_FEED)
        AegisTab.Protection -> navController.navigate(AegisDestinations.PROTECTION_DASHBOARD)
        AegisTab.Assistant -> navController.navigate(AegisDestinations.ASSISTANT_HOME)
        AegisTab.Settings -> navController.navigate(AegisDestinations.TASK_CENTER)
    }
}
