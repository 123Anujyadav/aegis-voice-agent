/**
 * Commit message linting (Phase 1 §8).
 *
 * Enforced in CI rather than only by a local hook. Hooks are bypassable with
 * --no-verify and, more importantly, are not installed by everyone: this is a
 * polyglot repository and a Go or Python engineer has no reason to have run
 * `pnpm install`. A convention enforced only by a hook is a convention that
 * holds for some of the team.
 *
 * WHY CONVENTIONAL COMMITS AT ALL: the type and scope drive automated changelog
 * generation and release-note routing per artefact (Phase 1 §15). Without a
 * machine-readable subject line, release notes for fourteen independently
 * versioned services have to be written by hand, which means they stop being
 * written.
 */

/**
 * The closed set of permitted scopes.
 *
 * Scopes are enumerated rather than free-form because open scopes drift —
 * `telephony`, `telephony-gw` and `tg` all appear within a quarter — and the
 * drift destroys the changelog grouping the convention exists to enable.
 *
 * Adding a scope is a deliberate act: a new service adds its scope here in the
 * same pull request that creates it.
 */
const scopes = [
  // Cross-cutting
  "contracts",
  "ci",
  "infra",
  "docker",
  "docs",
  "deps",
  "tooling",
  "release",

  // Shared packages
  "go-platform",
  "go-eventbus",
  "py-platform",
  "py-ai-primitives",
  "contracts-go",
  "contracts-py",
  "contracts-kt",
  "contracts-ts",

  // Go services
  "telephony-gateway",
  "media-relay",
  "session-orchestrator",
  "identity",
  "contacts-sync",
  "notification-fanout",
  "billing",
  "edge-api",

  // Python services
  "ai-orchestrator",
  "asr-gateway",
  "tts-gateway",
  "fraud-engine",
  "transcript-service",
  "prompt-registry",

  // Android
  "android",
  "android-app",
  "core-common",
  "core-model",
  "core-designsystem",
  "core-ui",
  "core-network",
  "core-database",
  "core-datastore",
  "core-telephony",
  "core-security",
  "core-analytics",
  "core-logging",
  "core-notifications",
  "core-permissions",
  "core-testing",
  "build-logic",
];

export default {
  extends: ["@commitlint/config-conventional"],
  rules: {
    "type-enum": [
      2,
      "always",
      [
        "feat", // a user-visible capability
        "fix", // a defect correction
        "perf", // a change made for measured performance reasons
        "refactor", // behaviour-preserving restructuring
        "docs",
        "test",
        "build", // build system or dependency changes
        "ci",
        "chore",
        "revert",
      ],
    ],

    // A scope is REQUIRED. In a monorepo an unscoped commit gives no indication
    // of which of fourteen services it touched, which makes both the changelog
    // and `git log` archaeology substantially less useful.
    "scope-empty": [2, "never"],
    "scope-enum": [2, "always", scopes],

    "subject-case": [2, "never", ["sentence-case", "start-case", "pascal-case", "upper-case"]],
    "subject-empty": [2, "never"],
    "subject-full-stop": [2, "never", "."],

    // 72 characters so that `git log --oneline` stays readable in an 80-column
    // terminal once the hash and decoration are accounted for.
    "header-max-length": [2, "always", 72],

    // The body explains WHY. It is wrapped at 100 so it renders correctly in
    // both a terminal and the GitHub UI.
    "body-max-line-length": [2, "always", 100],
    "body-leading-blank": [2, "always"],
    "footer-leading-blank": [2, "always"],
  },

  /**
   * Ignore commits that are generated rather than authored.
   *
   * Merge and revert commits have a fixed format produced by git itself, and
   * failing them would block the very operations used to recover from a bad
   * merge — exactly when the process must not obstruct.
   */
  ignores: [
    (message) => message.startsWith("Merge "),
    (message) => message.startsWith("Revert "),
    (message) => message.startsWith("chore(release):"),
  ],

  helpUrl: "https://github.com/callscreen/callscreen-platform/blob/main/CONTRIBUTING.md#commits",
};
