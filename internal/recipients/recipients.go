// Package recipients loads or creates the controlled payee keys.
//
// Generated private keys are written to the data directory at mode 0600 and
// are never logged. They exist so the operator can later internalize the
// synthetic outputs. They are not merchant keys.
package recipients

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
)

const fileName = "recipient-keys.json"

// File is the on-disk key set.
type File struct {
	Synthetic  bool    `json:"synthetic"`
	Note       string  `json:"note"`
	Recipients []Entry `json:"recipients"`
}

// Entry is one controlled recipient. PrivateKeyHex is absent for operator-supplied
// public keys that this process did not generate.
type Entry struct {
	PrivateKeyHex string `json:"private_key_hex,omitempty"`
	IdentityKey   string `json:"identity_key"`
}

// Ensure returns the public keys payments may use. Existing files are kept.
func Ensure(dir string, generate int, configured []string) ([]*ec.PublicKey, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, fileName)
	var doc File
	buf, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(buf, &doc); err != nil {
			return nil, errors.New("recipient-keys.json is not valid JSON")
		}
	case os.IsNotExist(err):
		doc = File{
			Synthetic: true,
			Note:      "Controlled test recipient keys for synthetic BRC-29 load. Not merchant keys.",
		}
		for i := 0; i < generate; i++ {
			key, err := ec.NewPrivateKey()
			if err != nil {
				return nil, err
			}
			doc.Recipients = append(doc.Recipients, Entry{
				PrivateKeyHex: key.Hex(),
				IdentityKey:   key.PubKey().ToDERHex(),
			})
		}
		if len(doc.Recipients) > 0 {
			raw, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
				return nil, err
			}
		}
	default:
		return nil, err
	}
	seen := map[string]struct{}{}
	var pubs []*ec.PublicKey
	for _, e := range doc.Recipients {
		pub, err := ec.PublicKeyFromString(e.IdentityKey)
		if err != nil {
			return nil, errors.New("recipient-keys.json contains an invalid identity key")
		}
		if _, ok := seen[pub.ToDERHex()]; ok {
			continue
		}
		seen[pub.ToDERHex()] = struct{}{}
		pubs = append(pubs, pub)
	}
	for _, hexKey := range configured {
		pub, err := ec.PublicKeyFromString(hexKey)
		if err != nil {
			return nil, errors.New("configured recipient identity is invalid")
		}
		if _, ok := seen[pub.ToDERHex()]; ok {
			continue
		}
		seen[pub.ToDERHex()] = struct{}{}
		pubs = append(pubs, pub)
	}
	if len(pubs) == 0 {
		return nil, errors.New("no controlled recipients")
	}
	return pubs, nil
}
