# The app's baseline profile (production-readiness F10)

**What it is:** a list of the classes and methods that a cold start and the first screens run.
- **Who uses it:** Android compiles them ahead of time when the app is installed, instead of interpreting them the first time they run. That makes the first launches faster.
- **How it reaches the phone:**
  - Google Play delivers it with the app;
  - for a sideloaded or emulator install, `androidx.profileinstaller`, already a dependency, writes it after the first launch.

**Status, 2026-11-04:**
- **Generated and committed:** `android/app/src/main/baseline-prof.txt`, 16,450 rules.
- **Recorded on:** an Android 9 emulator (Google APIs image, rooted).
- **Installation is verified on that emulator.**
- **Not done:**
  - **the startup gain is not measured:** emulator timings do not represent a phone;
  - **no physical device has run it** (X3).

## How it is made

`android/baselineprofile` is a self-instrumenting test module, like `test-minified`. It drives the app's **`profile`** build type from outside, with `BaselineProfileRule` from `androidx.benchmark:benchmark-macro-junit4`. That library was already in the version catalog; **no new dependency or Gradle plugin was added.**

**The `profile` build type** (`AndroidApplicationConventionPlugin`) is:
- release's configuration, **without R8**, so the recorded rules name classes and methods as the source does;
- not debuggable, so the code runs as release code does;
- debug-signed, with the application id `com.callscreen.profile`;
- **never shipped.**

**What the release build does with it:** the Android Gradle Plugin merges the committed profile with the libraries' own profiles (Compose, AndroidX). R8 then rewrites the result to the names it assigns, and it is packaged as `assets/dexopt/baseline.prof`.

**The recorded journey:**
1. a cold start through the splash to Welcome;
2. Welcome's Language control to the app-language screen (A58l), and back;
3. Get Protected to SIM detection, then Verify Phone Number to the sign-in entry;
4. back to Welcome.

**Limit of a build without identity endpoints:** that is as far as the app goes. Screens after sign-in benefit from the shared code in the profile (Compose, navigation, the design system), but **their own code is not in it.** Regenerate once a signed-in journey can be recorded (after E2 and X5).

**To regenerate**, on an emulator or device running Android 13 or later, or any rooted one:

```sh
cd android
./gradlew :baselineprofile:connectedProfileAndroidTest \
  -Pandroid.testInstrumentationRunnerArguments.androidx.benchmark.enabledRules=BaselineProfile
cp baselineprofile/build/outputs/connected_android_test_additional_output/profile/connected/*/BaselineProfileGenerator_generate-baseline-prof.txt \
  app/src/main/baseline-prof.txt
```

Review the diff like code. CI compiles the generator (`pr-android`) but does not run it.

**Why not record from the staging build:** staging is R8-minified. A profile recorded from it names classes as R8 renamed them (`LA/A;`), and those names change with every build. The first attempt did exactly that, and was discarded.

## Evidence, 2026-11-04

| Check | Result |
|---|---|
| Generator run | Android 9 emulator (`Aegis_API_28`, rooted): 1 test passed; 16,450 rules, of which 735 are the app's own code (`com.callscreen`) and the rest the libraries it runs |
| Merged into the staging (R8) build | After R8's rewriting, **10,773 rules with the app's profile against 8,803 from the libraries alone.** `assets/dexopt/baseline.prof` grows from 4,359 to 5,516 bytes |
| APK size gate | 1,399,558 bytes, the same as without the profile (4 KB alignment). Languages `en hi kn ml ta te` |
| Installed on a device | Staging APK on the Android 9 emulator: `ProfileInstaller: Installing profile for com.callscreen.staging`. The app's current profile went from 0 to 5,511 bytes after the first launch |
| Startup time | **Not measured.** An emulator's timing does not represent a phone. A startup macrobenchmark belongs with a physical device (X3) |
