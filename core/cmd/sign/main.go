// Command sign makes the signatures the desktop app demands of an update:
// ed25519 over the archive's SHA-256, with the key whose public half is
// updateKey in desktop/update.go.
//
//	sign gen <env-file>   writes a new private key there, prints the public one
//	sign <archive>        prints the archive's signature; the private key
//	                      comes from $CANALPEER_UPDATE_KEY
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	switch {
	case len(args) == 2 && args[0] == "gen":
		pub, priv, err := ed25519.GenerateKey(nil)
		if err != nil {
			return err
		}
		// O_EXCL: a key already there signs the releases installed apps accept.
		f, err := os.OpenFile(args[1], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(f, "CANALPEER_UPDATE_KEY=%s\n", base64.StdEncoding.EncodeToString(priv.Seed())); err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
		return nil
	case len(args) == 1:
		seed, err := base64.StdEncoding.DecodeString(os.Getenv("CANALPEER_UPDATE_KEY"))
		if err != nil || len(seed) != ed25519.SeedSize {
			return fmt.Errorf("CANALPEER_UPDATE_KEY is not a key")
		}
		f, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		fmt.Println(base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.NewKeyFromSeed(seed), h.Sum(nil))))
		return nil
	}
	return fmt.Errorf("usage: sign gen <env-file> | sign <archive>")
}
