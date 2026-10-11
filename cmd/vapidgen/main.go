// Command vapidgen prints a new VAPID key pair for Web Push. The public key
// line goes into deploy/.env as is; the private key line goes, alone, into
// the secret file MEALQUEST_VAPID_PRIVATE_KEY_FILE names. Both keys are
// unpadded base64url P-256 keys, the encoding config.Load validates.
//
//	go run ./cmd/vapidgen
package main

import (
	"fmt"
	"io"
	"os"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/config"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/vapid"
)

func main() {
	if len(os.Args) > 1 {
		fmt.Fprintln(os.Stderr, "usage: vapidgen (takes no arguments)")
		os.Exit(2)
	}
	if err := run(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run writes a new key pair to w in four lines: a comment and the public
// key's environment line, then a comment and the private key alone.
func run(w io.Writer) error {
	pair, err := vapid.Generate()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "# Environment (deploy/.env):\n%s=%s\n# Private key: this next line alone is the file %s names.\n%s\n",
		config.EnvVAPIDPublicKey, pair.PublicKey, config.EnvVAPIDPrivateKeyFile, pair.PrivateKey)
	return err
}
