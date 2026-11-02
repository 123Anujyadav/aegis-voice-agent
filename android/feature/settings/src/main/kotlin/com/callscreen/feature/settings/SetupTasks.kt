package com.callscreen.feature.settings

import androidx.annotation.StringRes
import com.callscreen.core.telephony.screening.ScreeningRoleStatus

/**
 * The Settings tab's checklist, from the phone's actual state only: the call
 * screening role, contacts access, and call forwarding (not available yet:
 * ADR-0002's forwarding needs a platform number that does not exist).
 * Its words are string resources (res/values/strings.xml).
 */
public object SetupTasks {
    public fun of(role: ScreeningRoleStatus, contactsAccess: Boolean): List<TaskItem> = listOf(
        when (role) {
            ScreeningRoleStatus.HELD -> TaskItem(BLOCKING, R.string.settings_task_blocking_done, TaskStatus.DONE)
            ScreeningRoleStatus.NOT_HELD -> TaskItem(BLOCKING, R.string.settings_task_blocking_to_do, TaskStatus.TO_DO)
            ScreeningRoleStatus.UNAVAILABLE ->
                TaskItem(BLOCKING, R.string.settings_task_blocking_unavailable, TaskStatus.NOT_AVAILABLE)
        },
        when {
            role == ScreeningRoleStatus.UNAVAILABLE ->
                TaskItem(CONTACTS, R.string.settings_task_contacts_unavailable, TaskStatus.NOT_AVAILABLE)
            contactsAccess -> TaskItem(CONTACTS, R.string.settings_task_contacts_done, TaskStatus.DONE)
            else -> TaskItem(CONTACTS, R.string.settings_task_contacts_to_do, TaskStatus.TO_DO)
        },
        TaskItem(FORWARDING, R.string.settings_task_forwarding_unavailable, TaskStatus.NOT_AVAILABLE),
    )

    @StringRes
    public fun label(status: TaskStatus): Int = when (status) {
        TaskStatus.DONE -> R.string.settings_task_status_done
        TaskStatus.TO_DO -> R.string.settings_task_status_to_do
        TaskStatus.NOT_AVAILABLE -> R.string.settings_task_status_not_available
    }

    @StringRes public val BLOCKING: Int = R.string.settings_task_blocking

    @StringRes public val CONTACTS: Int = R.string.settings_task_contacts

    @StringRes public val FORWARDING: Int = R.string.settings_task_forwarding
}
