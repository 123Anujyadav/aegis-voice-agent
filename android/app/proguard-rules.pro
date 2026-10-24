# R8 rules for the app module (release and staging, which inherits release).
#
# Intentionally empty for now. The libraries in use ship their own consumer
# rules (OkHttp, kotlinx.serialization, Hilt/Dagger, Compose, DataStore, Play
# Integrity), and the app has no reflection of its own.
#
# The pr-android workflow builds the minified staging variant, so a rule that
# is genuinely missing fails CI here. Add a rule only for a specific R8
# finding, with a comment that names it.
