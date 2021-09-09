package types

type ConsoleAuth interface {
	Authenticate(user string, password string) bool
	RequireAuthentication() bool
}
