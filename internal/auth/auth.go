package auth

type Policy struct {
	TenantID          string
	ServiceName       string
	AllowedMethods    []string
	AllowedPathPrefix []string
}

func Authorize() bool {
	return false
}
