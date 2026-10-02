// e3-keyring checks whether an item written by zalando/go-keyring can be read
// by another program (here /usr/bin/security) without a confirmation dialog.
package main

import (
	"fmt"
	"os"

	"github.com/zalando/go-keyring"
)

func main() {
	const svc, user = "farero-exp", "probe"
	switch os.Args[1] {
	case "set":
		fmt.Println("set:", keyring.Set(svc, user, "s3cret-value"))
	case "get":
		v, err := keyring.Get(svc, user)
		fmt.Println("get:", v, err)
	case "del":
		fmt.Println("del:", keyring.Delete(svc, user))
	}
}
