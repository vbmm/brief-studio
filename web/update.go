package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const releasesAPI = "https://api.github.com/repos/vbmm/brief-studio/releases/latest"
const releasesPage = "https://github.com/vbmm/brief-studio/releases/tag/"

var stableTag = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

type githubRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

type updateResult struct {
	Available bool   `json:"available"`
	Tag       string `json:"tag,omitempty"`
	URL       string `json:"url,omitempty"`
}

func checkUpdate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPI, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "GitHub est indisponible.")
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "Impossible de vérifier les versions sur GitHub.")
		return
	}
	defer response.Body.Close()
	var release githubRelease
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&release) != nil {
		writeError(w, http.StatusBadGateway, "Impossible de vérifier les versions sur GitHub.")
		return
	}
	writeJSON(w, http.StatusOK, release.webUpdate(version))
}

func (release githubRelease) webUpdate(installed string) updateResult {
	if release.Draft || release.Prerelease || !newerVersion(release.Tag, installed) {
		return updateResult{}
	}
	for _, asset := range release.Assets {
		if strings.HasPrefix(asset.Name, "Brief-Studio-"+strings.TrimPrefix(release.Tag, "v")+"-Web-") && strings.HasSuffix(asset.Name, ".zip") {
			return updateResult{true, release.Tag, releasesPage + release.Tag}
		}
	}
	return updateResult{}
}

func newerVersion(newTag, currentTag string) bool {
	if !stableTag.MatchString(newTag) || !stableTag.MatchString(currentTag) {
		return false
	}
	newParts := strings.Split(strings.TrimPrefix(newTag, "v"), ".")
	currentParts := strings.Split(strings.TrimPrefix(currentTag, "v"), ".")
	for i := range newParts {
		v, err := strconv.ParseUint(newParts[i], 10, 32)
		old, oldErr := strconv.ParseUint(currentParts[i], 10, 32)
		if err != nil || oldErr != nil {
			return false
		}
		if v != old {
			return v > old
		}
	}
	return false
}
