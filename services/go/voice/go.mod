module github.com/callscreen/callscreen-platform/services/go/voice

// 1.25.0 rather than 1.23.0 because this service now depends on the AI-plane
// modules, which declare 1.25.0. A module cannot depend on one whose targeted
// version exceeds its own. CI already builds with GO_VERSION 1.25.x.
go 1.25.0

require (
	github.com/callscreen/callscreen-platform/packages/go/platform v0.0.0
	github.com/callscreen/callscreen-platform/packages/go/voiceintel v0.0.0
)

require (
	github.com/callscreen/callscreen-platform/packages/go/audiobridge v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/audiointel v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/conversation v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/governance v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/intent v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/media v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/metrics v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/runtime v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/speech v0.0.0 // indirect
	github.com/callscreen/callscreen-platform/packages/go/voice v0.0.0 // indirect
)

replace github.com/callscreen/callscreen-platform/packages/go/audiobridge => ../../../packages/go/audiobridge

replace github.com/callscreen/callscreen-platform/packages/go/audiointel => ../../../packages/go/audiointel

replace github.com/callscreen/callscreen-platform/packages/go/conversation => ../../../packages/go/conversation

replace github.com/callscreen/callscreen-platform/packages/go/governance => ../../../packages/go/governance

replace github.com/callscreen/callscreen-platform/packages/go/intent => ../../../packages/go/intent

replace github.com/callscreen/callscreen-platform/packages/go/media => ../../../packages/go/media

replace github.com/callscreen/callscreen-platform/packages/go/metrics => ../../../packages/go/metrics

replace github.com/callscreen/callscreen-platform/packages/go/platform => ../../../packages/go/platform

replace github.com/callscreen/callscreen-platform/packages/go/runtime => ../../../packages/go/runtime

replace github.com/callscreen/callscreen-platform/packages/go/speech => ../../../packages/go/speech

replace github.com/callscreen/callscreen-platform/packages/go/voice => ../../../packages/go/voice

replace github.com/callscreen/callscreen-platform/packages/go/voiceintel => ../../../packages/go/voiceintel
