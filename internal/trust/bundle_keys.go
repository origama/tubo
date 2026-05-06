package trust

const (
	DefaultPublicNetworkName      = "tubo-public"
	DefaultPublicNetworkBundleURL = "https://tubo.click/.well-known/tubo/networks/tubo-public.bundle"
)

var BundleSigningKeys = map[string]string{
	"tubo-root-2026": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEMmu4uNA2C/KKW1VX/1Cr/PSasaa8bvi9ExjBhNqltQ bettersafethansorry@tubo.click",
}
