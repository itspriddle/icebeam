package credentials

import "fmt"

// ResticPasswordEnv returns the environment variable(s) that hand the repository
// password to restic without ever placing it on the command line (argv leaks via
// the process table). Each element is a "KEY=value" string suitable for appending
// to an exec.Cmd's Env.
//
//   - File backend: RESTIC_PASSWORD_FILE points restic at the 0600 password
//     file, so the secret itself never enters this process's environment either.
//   - Keychain backend: the password is read from the keychain and passed via
//     RESTIC_PASSWORD in the child's environment — still not on argv.
//
// In both cases the returned strings are intended for exec.Cmd.Env, never for
// exec.Cmd.Args.
func ResticPasswordEnv(store CredentialStore) ([]string, error) {
	if fs, ok := store.(*fileStore); ok {
		return []string{"RESTIC_PASSWORD_FILE=" + fs.PasswordFilePath()}, nil
	}

	password, err := store.Get(RepoPassword)
	if err != nil {
		return nil, fmt.Errorf("credentials: load repository password: %w", err)
	}

	return []string{"RESTIC_PASSWORD=" + password}, nil
}
