module github.com/aiii-dot-id/aeon-memory

go 1.25

require github.com/aiii-dot-id/aii-plugin-sdk v0.0.0-20260910095813-13b764a0ff18

require (
	github.com/cloudflare/circl v1.6.3 // indirect
	golang.org/x/crypto v0.30.0 // indirect
	golang.org/x/sys v0.28.0 // indirect
)

tool github.com/aiii-dot-id/aii-plugin-sdk/cmd/aiisdk

// Development rides the local kit tree: the memory instruments (sdk.Memory)
// landed in 8ca7677, which is not yet resolvable through the public module
// proxy or GOPROXY=direct from this sandbox. The replace drops the day the
// pseudo-version v0.0.0-20260910211819-8ca7677f61e02efc resolves publicly;
// the require advances to it in the same edit.
replace github.com/aiii-dot-id/aii-plugin-sdk => ../aii-plugin-sdk
