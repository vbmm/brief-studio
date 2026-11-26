package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"briefstudio/web/internal/chatgpt"
	"briefstudio/web/internal/sources"
)

var version = "0.3.0"

//go:embed ui/*
var webAssets embed.FS

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	dataDir := flag.String("data-dir", "", "dossier des briefs locaux")
	noBrowser := flag.Bool("no-browser", false, "afficher le lien sans ouvrir de navigateur")
	flag.Parse()
	if *dataDir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return errors.New("impossible de trouver le dossier utilisateur")
		}
		if runtime.GOOS == "windows" && os.Getenv("LOCALAPPDATA") != "" {
			base = os.Getenv("LOCALAPPDATA")
		}
		*dataDir = filepath.Join(base, "Brief Studio Web")
	}
	archive, err := openLibrary(*dataDir)
	if err != nil {
		return fmt.Errorf("ouvrir la bibliothèque locale : %w", err)
	}
	assets, err := fs.Sub(webAssets, "ui")
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("ouvrir la boucle locale : %w", err)
	}
	defer listener.Close()
	app := &studioServer{
		origin: "http://" + listener.Addr().String(), host: listener.Addr().String(),
		bootKey: randomKey(), browserKey: randomKey(), cookieName: fmt.Sprintf("brief_studio_%d", listener.Addr().(*net.TCPAddr).Port),
		library: archive, client: chatgpt.New(), assets: assets,
		sourceSettings: openSourceStore(*dataDir), fetchSource: sources.New().Fetch,
	}
	server := &http.Server{
		Handler: app.handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 160 * time.Second,
		IdleTimeout: 45 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-shutdown.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if server.Shutdown(ctx) != nil {
			_ = server.Close()
		}
	}()
	entry := app.origin + "/#key=" + app.bootKey
	fmt.Printf("Brief Studio Web %s\nOuvre ce lien dans ton navigateur :\n%s\nFerme cette fenêtre ou appuie sur Ctrl+C pour arrêter l’app.\n", version, entry)
	if !*noBrowser {
		go func() {
			if err := openBrowser(entry); err != nil {
				fmt.Println("Ouvre le lien affiché si ton navigateur ne s’est pas ouvert.")
			}
		}()
	}
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func openBrowser(localURL string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("/usr/bin/open", localURL).Run()
	case "windows":
		return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", localURL).Run()
	default:
		return exec.Command("xdg-open", localURL).Run()
	}
}

func randomKey() string {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		panic("le système ne peut pas générer de clé locale")
	}
	return base64.RawURLEncoding.EncodeToString(key[:])
}
