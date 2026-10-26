package com.callscreen.feature.settings

import com.callscreen.core.telephony.screening.ScreeningRoleStatus

/**
 * The Settings tab's checklist, from the phone's actual state only: the call
 * screening role, contacts access, and call forwarding (not available yet:
 * ADR-0002's forwarding needs a platform number that does not exist).
 */
public object SetupTasks {
    public fun of(role: ScreeningRoleStatus, contactsAccess: Boolean): List<TaskItem> = listOf(
        when (role) {
            ScreeningRoleStatus.HELD -> TaskItem(BLOCKING, "Numbers you block are rejected on this phone.", TaskStatus.DONE)
            ScreeningRoleStatus.NOT_HELD ->
                TaskItem(BLOCKING, "Turn it on from Protection, Blocked numbers.", TaskStatus.TO_DO)
            ScreeningRoleStatus.UNAVAILABLE ->
                TaskItem(BLOCKING, "This version of Android doesn't let apps block calls.", TaskStatus.NOT_AVAILABLE)
        },
        when {
            role == ScreeningRoleStatus.UNAVAILABLE ->
                TaskItem(CONTACTS, "Needs on-device blocking, which this phone can't do.", TaskStatus.NOT_AVAILABLE)
            contactsAccess ->
                TaskItem(CONTACTS, "Saved contacts you block are rejected too. Contacts are never read or uploaded.", TaskStatus.DONE)
            else ->
                TaskItem(CONTACTS, "Optional. Allow contacts access from Protection, Blocked numbers.", TaskStatus.TO_DO)
        },
        TaskItem(
            FORWARDING,
            "Needs a number from our service, which isn't set up yet. Nothing has been changed with your carrier.",
            TaskStatus.NOT_AVAILABLE,
        ),
    )

    public fun label(status: TaskStatus): String = when (status) {
        TaskStatus.DONE -> "Done"
        TaskStatus.TO_DO -> "To do"
        TaskStatus.NOT_AVAILABLE -> "Not available"
    }

    public const val BLOCKING: String = "On-device blocking"
    public const val CONTACTS: String = "Block saved contacts"
    public const val FORWARDING: String = "Call forwarding to the assistant"
}
