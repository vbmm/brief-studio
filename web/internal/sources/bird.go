package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const walterHandle = "deitaone"

// Bird invokes the installed macOS CLI with its existing Chrome profile.
// Brief Studio consumes only the returned public post references.
type Bird struct{ path string }

func FindBird() *Bird {
	if runtime.GOOS != "darwin" {
		return nil
	}
	for _, path := range []string{"/opt/homebrew/bin/bird", "/usr/local/bin/bird"} {
		if file, err := os.Stat(path); err == nil && file.Mode().IsRegular() && file.Mode().Perm()&0111 != 0 {
			return &Bird{path: path}
		}
	}
	if path, err := exec.LookPath("bird"); err == nil && filepath.IsAbs(path) {
		return &Bird{path: path}
	}
	return nil
}

func (bird *Bird) Refresh(ctx context.Context) (XSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 28*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, bird.path, "user-tweets", walterHandle, "-n", "15", "--json",
		"--timeout", "12000", "--cookie-timeout", "6000", "--cookie-source", "chrome")
	command.Env = birdChromeEnvironment(command.Environ(), filepath.Dir(bird.path))
	command.WaitDelay = 2 * time.Second
	output := &birdOutput{}
	command.Stdout, command.Stderr = output, io.Discard
	if err := command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return XSnapshot{}, sourceError{http.StatusGatewayTimeout, "La lecture Bird a dépassé le délai."}
		}
		return XSnapshot{}, errors.New("Bird n’a pas pu lire Walter. Vérifie ta connexion à X dans le profil Chrome configuré pour Bird.")
	}
	return parseBirdSnapshot(output.Bytes(), time.Now().UTC())
}

func birdChromeEnvironment(environment []string, binaryDir string) []string {
	accepted := make([]string, 0, len(environment))
	path := binaryDir
	for _, variable := range environment {
		name, value, _ := strings.Cut(variable, "=")
		switch name {
		case "PATH":
			if value != "" {
				path = binaryDir + string(os.PathListSeparator) + value
			}
		case "HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE", "XDG_CONFIG_HOME":
			accepted = append(accepted, variable)
		}
	}
	return append(accepted, "PATH="+path)
}

type birdOutput struct{ bytes.Buffer }

func (output *birdOutput) Write(data []byte) (int, error) {
	if output.Len()+len(data) > 1<<20 {
		return 0, errors.New("Réponse Bird trop volumineuse.")
	}
	return output.Buffer.Write(data)
}

func parseBirdSnapshot(body []byte, cutoff time.Time) (XSnapshot, error) {
	var rows []struct {
		ID        string `json:"id"`
		CreatedAt string `json:"createdAt"`
		Author    struct {
			Username  string `json:"username"`
			Protected bool   `json:"protected"`
		} `json:"author"`
	}
	if err := json.Unmarshal(body, &rows); err != nil || len(rows) == 0 {
		return XSnapshot{}, errors.New("Bird n’a pas fourni de publications lisibles.")
	}
	snapshot := XSnapshot{Schema: XSnapshotSchema, ObservedAt: cutoff, Posts: []XSnapshotPost{}}
	seen := make(map[string]bool)
	for _, row := range rows {
		if !xID.MatchString(row.ID) || seen[row.ID] || row.Author.Protected || !strings.EqualFold(row.Author.Username, walterHandle) {
			continue
		}
		published, err := time.Parse(time.RFC3339Nano, row.CreatedAt)
		if err != nil {
			published, err = time.Parse("Mon Jan 2 15:04:05 -0700 2006", row.CreatedAt)
		}
		if err != nil || published.Before(cutoff.Add(-xMaxAge)) || published.After(cutoff) {
			continue
		}
		seen[row.ID] = true
		snapshot.Posts = append(snapshot.Posts, XSnapshotPost{ID: row.ID, Account: walterHandle, PublishedAt: published.UTC()})
		if len(snapshot.Posts) == xResultLimit {
			break
		}
	}
	if len(snapshot.Posts) == 0 {
		return XSnapshot{}, errors.New("Bird n’a trouvé aucune publication récente de Walter.")
	}
	return snapshot, nil
}
