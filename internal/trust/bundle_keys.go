package trust

const (
	DefaultPublicNetworkName      = "tubo-public"
	DefaultPublicNetworkBundleURL = "https://www.tubo.click/.well-known/tubo/networks/tubo-public.bundle"
)

var BundleSigningKeys = map[string]string{
	"tubo-root-2026": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIEA8cMzoOQb4clMnL7m4Rrp0RzAQXXCCT40PY1DYBOd root@localhost",
}
