package schedule

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const (
	fileVersion = 1
	fileLimit   = 16 << 10
)

var (
	errRead = errors.New("Le fichier de planification local est illisible ou invalide.")
	errSave = errors.New("Impossible d’enregistrer la planification.")
)

type diskState struct {
	Version     int       `json:"version"`
	Config      Config    `json:"config"`
	Last        *Result   `json:"last,omitempty"`
	ArmedAt     time.Time `json:"armedAt"`
	ClaimedDate string    `json:"claimedDate,omitempty"`
}

func (data diskState) state() State {
	result := State{Config: data.Config, armedAt: data.ArmedAt, claimedDate: data.ClaimedDate}
	if data.Last != nil {
		copy := *data.Last
		result.Last = &copy
	}
	return result
}

func load(path string) (diskState, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return diskState{
			Version: fileVersion,
			Config:  Config{Time: "13:30", WeekdaysOnly: true, GEXMode: "without"},
		}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > fileLimit ||
		(runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return diskState{}, errRead
	}

	file, err := os.Open(path)
	if err != nil {
		return diskState{}, errRead
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, fileLimit+1))
	decoder.DisallowUnknownFields()
	var data diskState
	if decoder.Decode(&data) != nil || decoder.Decode(new(any)) != io.EOF ||
		data.Version != fileVersion || validateConfig(data.Config) != nil ||
		(data.Config.Enabled && data.ArmedAt.IsZero()) || !validSavedResult(data) {
		return diskState{}, errRead
	}
	return data, nil
}

func validSavedResult(data diskState) bool {
	if data.Last == nil {
		return data.ClaimedDate == ""
	}
	if _, err := time.Parse(dateLayout, data.ClaimedDate); err != nil || data.Last.At.IsZero() {
		return false
	}
	switch data.Last.Status {
	case "running", "success", "failed", "blocked", "cancelled", "interrupted":
		return validText(data.Last.BriefID, 128, false) && validText(data.Last.Message, 1024, false)
	default:
		return false
	}
}

func save(path string, data diskState) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return errSave
	}
	file, err := os.CreateTemp(directory, ".schedule-*")
	if err != nil {
		return errSave
	}
	defer os.Remove(file.Name())
	writeErr := json.NewEncoder(file).Encode(data)
	syncErr := file.Sync()
	if errors.Join(writeErr, syncErr, file.Close()) != nil || os.Rename(file.Name(), path) != nil {
		return errSave
	}
	return nil
}
