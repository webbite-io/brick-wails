// Command brick-fakes serves the in-memory fake accounts/OIDC API and Storage
// API on fixed loopback ports, so the app can be run and onboarded by hand
// (or smoke-tested) without the real backend. The fake IdP auto-approves
// logins. Point the app at it with the env it prints, e.g.:
//
//	go run ./internal/testutil/cmd/brick-fakes -seed
//	ACC_API_URL=http://127.0.0.1:18080 STORAGE_API_URL=http://127.0.0.1:18081 \
//	OAUTH_CLIENT_ID=test-client BRICK_CONFIG_DIR=/tmp/brick-dev ./bin/brick-ui
//
// Always use BRICK_CONFIG_DIR (and ideally XDG_RUNTIME_DIR) with it, so the
// app doesn't touch your real brick config.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"

	"github.com/webbite-io/brick-wails/internal/testutil/fakeoidc"
	"github.com/webbite-io/brick-wails/internal/testutil/fakestorage"
)

func main() {
	oidcAddr := flag.String("oidc", "127.0.0.1:18080", "accounts/OIDC API listen address")
	storageAddr := flag.String("storage", "127.0.0.1:18081", "Storage API listen address")
	seed := flag.Bool("seed", false, "seed the remote tree with a few folders and files")
	flag.Parse()

	ol, err := net.Listen("tcp", *oidcAddr)
	if err != nil {
		log.Fatal(err)
	}
	sl, err := net.Listen("tcp", *storageAddr)
	if err != nil {
		log.Fatal(err)
	}
	idp := fakeoidc.NewOn(ol)
	defer idp.Close()
	fs := fakestorage.NewOn(sl, "acct-1")
	defer fs.Close()
	fs.Authorize = idp.ValidAccess
	if *seed {
		fs.PutFile("Documents/readme.txt", "Hello from the fake Brick storage.\n")
		fs.PutFile("Documents/notes/todo.txt", "- try the wizard\n")
		fs.PutFile("Photos/beach.jpg", "not really a jpeg")
		fs.PutFile("welcome.txt", "Welcome to Brick!\n")
	}

	fmt.Printf("ACC_API_URL=%s\nSTORAGE_API_URL=%s\nOAUTH_CLIENT_ID=%s\n", idp.URL, fs.URL, idp.ClientID)
	fmt.Fprintln(os.Stderr, "fakes running; Ctrl+C to stop")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	fmt.Fprintln(os.Stderr, "remote tree:", fs.Paths())
}
