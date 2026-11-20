package attest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
)

// StatusURL is Google's attestation certificate revocation list.
const StatusURL = "https://android.googleapis.com/attestation/status"

// StatusList is that list in memory. Entries are keyed by certificate serial
// number. The published file mixes lowercase hex and decimal keys, so a
// lookup tries both.
type StatusList struct {
	entries map[string]struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
}

func ParseStatusList(r io.Reader) (*StatusList, error) {
	var doc struct {
		Entries map[string]struct {
			Status string `json:"status"`
			Reason string `json:"reason"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, err
	}
	if doc.Entries == nil {
		return nil, fmt.Errorf("status list has no entries object")
	}
	return &StatusList{entries: doc.Entries}, nil
}

func LoadStatusList(file string) (*StatusList, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseStatusList(f)
}

func FetchStatusList(ctx context.Context, url string) (*StatusList, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status list: HTTP %d", resp.StatusCode)
	}
	return ParseStatusList(resp.Body)
}

func (s *StatusList) Len() int { return len(s.entries) }

// Revoked matches the signature of Options.Revoked.
func (s *StatusList) Revoked(serial *big.Int) (string, bool) {
	for _, key := range []string{strings.ToLower(serial.Text(16)), serial.Text(10)} {
		if e, ok := s.entries[key]; ok {
			return e.Status + " " + e.Reason, true
		}
	}
	return "", false
}
