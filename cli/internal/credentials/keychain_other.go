//go:build !darwin

package credentials

// On non-Darwin platforms the default store is the mode-0600 file backend.
// Native keyring integrations (gnome-keyring, KWallet) can land later as
// additional Store implementations.
func defaultStore() Store { return newFileStore() }
