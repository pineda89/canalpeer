// Command changelog serves core/changelog to the release scripts:
//
//	changelog ready <version>   fails unless the version has notes and a date
//	changelog notes <version>   its English notes, as GitHub release notes
//	changelog markdown          CHANGELOG.md for the public repo
package main

import (
	"fmt"
	"os"

	"canalpeer.com/core/changelog"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "ready", "notes":
		if len(os.Args) != 3 {
			usage()
		}
		if err := changelog.Ready(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if os.Args[1] == "notes" {
			for _, n := range changelog.Notes(os.Args[2], "en", "") {
				fmt.Printf("- %s\n", n)
			}
		}
	case "markdown":
		fmt.Print(changelog.Markdown())
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: changelog ready|notes <version> | changelog markdown")
	os.Exit(2)
}
